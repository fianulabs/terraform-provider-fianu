// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package notification

import (
	"slices"
	"strings"

	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	pkgvars "github.com/fianulabs/core/v2/external/pkg/variables"
)

// Every enum the notification schema validates against is derived from the
// SDK's registries at init time rather than restated here. When core adds a
// notification bucket, a channel, or a recipient role, the provider picks it
// up on the next SDK bump — no hardcoded list to forget.

var (
	podTypes   = db_vars.NotificationPodTypes()
	recipients = allRecipients()
	channels   = allChannels()
)

func podTypeStrings() []string {
	out := make([]string, len(podTypes))
	for i, p := range podTypes {
		out[i] = string(p)
	}
	slices.Sort(out)
	return out
}

func isNotificationPodType(s string) bool {
	return slices.Contains(podTypeStrings(), s)
}

func recipientStrings() []string {
	out := make([]string, len(recipients))
	for i, r := range recipients {
		out[i] = string(r)
	}
	slices.Sort(out)
	return out
}

func channelStrings() []string {
	out := make([]string, len(channels))
	for i, c := range channels {
		out[i] = string(c)
	}
	slices.Sort(out)
	return out
}

// allRecipients enumerates the role-scoped audiences. pkgvars keeps its
// registry unexported, so this reconstructs it from the exported constants —
// the IsValid check in the test guards against core adding one we missed.
func allRecipients() []pkgvars.Recipient {
	return []pkgvars.Recipient{
		pkgvars.RecipientControlOwner,
		pkgvars.RecipientControlManager,
		pkgvars.RecipientControlApprover,
		pkgvars.RecipientGateOwner,
		pkgvars.RecipientGateManager,
		pkgvars.RecipientGateApprover,
		pkgvars.RecipientAssetOwner,
	}
}

// allChannels enumerates the delivery channels. Same reconstruction rationale
// as allRecipients.
func allChannels() []pkgvars.Channel {
	return []pkgvars.Channel{
		pkgvars.ChannelEmail,
		pkgvars.ChannelInApp,
		pkgvars.ChannelSlack,
		pkgvars.ChannelWebhook,
		pkgvars.ChannelEvidenceStore,
		pkgvars.ChannelSCM,
	}
}

func podTypeDocList() string   { return backtickList(podTypeStrings()) }
func recipientDocList() string { return backtickList(recipientStrings()) }
func channelDocList() string   { return backtickList(channelStrings()) }

func backtickList(vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = "`" + v + "`"
	}
	return strings.Join(quoted, ", ")
}
