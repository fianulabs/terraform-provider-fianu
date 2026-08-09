// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package policy_test

import (
	"regexp"
	"testing"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccFianuPolicyException_Minimal covers the create/read/plan loop and the
// three things that make an exception a distinct resource rather than a
// fianu_policy with a different `type`: the deploy envelope carries
// entityType=policy_exception, the composite ID is prefixed with it, and
// detail.type defaults to "exception" without the user writing it.
func TestAccFianuPolicyException_Minimal(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigMinimalException,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "path", "test.exception.basic"),
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "detail.type", "exception"),
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "id", "policy_exception/test.exception.basic"),
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "uuid", "test-exception-uuid"),
				),
			},
			{
				Config: testAccConfigMinimalException,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured := stub.capturedOfType("policy_exception")
	if captured == nil {
		t.Fatalf("expected the stub to capture a deploy under entityType=policy_exception; it did not, so the resource deployed under the wrong type")
	}
	if captured.StandardEntity.Type != "policy_exception" {
		t.Errorf("entity.type = %q, want policy_exception", captured.StandardEntity.Type)
	}
	if captured.Detail.Type != fianu_entities.PolicyTypeException {
		t.Errorf("detail.type = %q, want exception", captured.Detail.Type)
	}
	if captured.Detail.Control.Path != "test.control.basic" {
		t.Errorf("control.path = %q, want test.control.basic", captured.Detail.Control.Path)
	}
}

const testAccConfigMinimalException = `
provider "fianu" {}

resource "fianu_policy_exception" "example" {
  path = "test.exception.basic"
  name = "Basic Test Exception"

  detail = {
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "exempt"
        priority = 10
        criteria = {
          asset = { type = "repository" }
        }
        policy   = jsonencode({ required = false })
      },
    ]
  }
}
`

// TestAccFianuPolicyException_Update proves an in-place change round-trips:
// the second deploy is not swallowed by the idempotency gate, and the changed
// variation lands on the wire.
func TestAccFianuPolicyException_Update(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigMinimalException,
				Check: resource.TestCheckResourceAttr(
					"fianu_policy_exception.example", "detail.variations.0.priority", "10"),
			},
			{
				Config: testAccConfigExceptionUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "detail.variations.0.priority", "42"),
					resource.TestCheckResourceAttr("fianu_policy_exception.example", "name", "Renamed Test Exception"),
				),
			},
		},
	})

	captured := stub.capturedOfType("policy_exception")
	if captured == nil {
		t.Fatal("expected a captured exception entity")
	}
	if got := captured.Detail.Variations[0].Priority; got != 42 {
		t.Errorf("after update, variation[0].priority = %d, want 42", got)
	}
	if captured.Name != "Renamed Test Exception" {
		t.Errorf("after update, name = %q, want Renamed Test Exception", captured.Name)
	}
}

const testAccConfigExceptionUpdated = `
provider "fianu" {}

resource "fianu_policy_exception" "example" {
  path = "test.exception.basic"
  name = "Renamed Test Exception"

  detail = {
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "exempt"
        priority = 42
        criteria = {
          asset = { type = "repository" }
        }
        policy   = jsonencode({ required = false })
      },
    ]
  }
}
`

// TestAccFianuPolicyException_Import proves the composite ID round-trips
// through `terraform import`, which requires ParseID to accept the
// policy_exception prefix and Read to hit the exceptions route.
func TestAccFianuPolicyException_Import(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigMinimalException},
			{
				Config:            testAccConfigMinimalException,
				ResourceName:      "fianu_policy_exception.example",
				ImportState:       true,
				ImportStateId:     "policy_exception/test.exception.basic",
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					// detail is user-authored and deliberately not hydrated on
					// Read — the server canonicalises ordering and defaults,
					// which would surface as drift. Same contract as
					// fianu_policy.
					"detail",
					// version.status/state/uuid come from a Read, not from the
					// deploy response (DeploymentMetadata carries no workflow
					// state), so post-apply state has them empty while the
					// imported state has the server's values. That asymmetry is
					// the documented contract for these three: they are the
					// only computed envelope fields without UseStateForUnknown,
					// precisely so each plan re-reads server-side workflow
					// state.
					"version.status",
					"version.state",
					"version.uuid",
				},
			},
		},
	})
}

// TestAccFianuPolicyException_ArchivesThroughExceptionRoute pins the delete
// path. Exceptions archive at /api/entities/archive/exception/:id; sending
// them to the policy archive route leaves the entity live while Terraform
// reports a successful destroy.
func TestAccFianuPolicyException_ArchivesThroughExceptionRoute(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigMinimalException},
		},
	})

	// resource.Test destroys at the end of the case, so by here the DELETE
	// has been issued.
	want := "/api/entities/archive/exception/test-exception-uuid"
	if _, ok := stub.archivedPaths.Load(want); !ok {
		var seen []string
		stub.archivedPaths.Range(func(k, _ any) bool {
			seen = append(seen, k.(string))
			return true
		})
		t.Errorf("expected a DELETE to %s; saw %v", want, seen)
	}
}

// TestAccFianuPolicy_RejectsExceptionType is the regression guard for the bug
// this resource split fixes. `fianu_policy` with detail.type = "exception"
// used to deploy successfully — the server routes on detail.type
// (pkg/entities_files/policy_deployer.go) and created a real exception — but
// Read then called FetchPolicy, which filters entityType=policy and never
// returns exceptions. Every refresh 404'd and silently evicted the resource
// from state. The type is now rejected at plan time.
func TestAccFianuPolicy_RejectsExceptionType(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigPolicyWithExceptionType,
				ExpectError: regexp.MustCompile(`(?s)Attribute detail\.type value must be one of.*standard.*target`),
			},
		},
	})
}

const testAccConfigPolicyWithExceptionType = `
provider "fianu" {}

resource "fianu_policy" "example" {
  path = "test.policy.wrongtype"
  name = "Wrong Type Policy"

  detail = {
    type = "exception"
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "apply"
        priority = 0
        criteria = {
          asset = { type = "repository" }
        }
        policy   = jsonencode({ required = true })
      },
    ]
  }
}
`

// TestAccFianuPolicyAndException_Coexist proves the two resources address
// different entities. A policy and an exception on the same control deploy
// under different entity types, get different UUIDs, and read back from their
// own routes without cross-contaminating state.
func TestAccFianuPolicyAndException_Coexist(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigCoexist,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_policy.base", "id", "policy/test.coexist.policy"),
					resource.TestCheckResourceAttr("fianu_policy.base", "uuid", "test-policy-uuid"),
					resource.TestCheckResourceAttr("fianu_policy_exception.waiver", "id", "policy_exception/test.coexist.exception"),
					resource.TestCheckResourceAttr("fianu_policy_exception.waiver", "uuid", "test-exception-uuid"),
				),
			},
			{
				Config: testAccConfigCoexist,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	pol := stub.capturedOfType("policy")
	exc := stub.capturedOfType("policy_exception")
	if pol == nil || exc == nil {
		t.Fatalf("expected both entity types captured; policy=%v exception=%v", pol != nil, exc != nil)
	}
	if pol.Path == exc.Path {
		t.Errorf("policy and exception captured the same path %q — the stub keyed them together", pol.Path)
	}
	if pol.Detail.Type != fianu_entities.PolicyTypeStandard {
		t.Errorf("policy detail.type = %q, want standard", pol.Detail.Type)
	}
	if exc.Detail.Type != fianu_entities.PolicyTypeException {
		t.Errorf("exception detail.type = %q, want exception", exc.Detail.Type)
	}
}

const testAccConfigCoexist = `
provider "fianu" {}

resource "fianu_policy" "base" {
  path = "test.coexist.policy"
  name = "Coexist Policy"

  detail = {
    type = "standard"
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "apply"
        priority = 0
        criteria = {
          asset = { type = "repository" }
        }
        policy   = jsonencode({ required = true })
      },
    ]
  }
}

resource "fianu_policy_exception" "waiver" {
  path = "test.coexist.exception"
  name = "Coexist Exception"

  detail = {
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "exempt"
        priority = 100
        criteria = {
          asset = { type = "repository" }
        }
        policy   = jsonencode({ required = false })
      },
    ]
  }
}
`
