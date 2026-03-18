// Package continuous_monitoring implements the sanctions.io enrollment at Stage 7.
//
// Called when FIS return file confirms RT30 Completed (card physically issued).
// After enrollment, sanctions.io monitors the member against its global watchlist
// (updated every 60 minutes) and pushes webhook alerts on list updates.
//
// This satisfies MVB § 10.3 "rescreen whenever lists are updated" precisely —
// no scheduled batch job, no cadence approximation, no exposure windows.
package continuous_monitoring

import "context"

// Enroller enrolls confirmed members in sanctions.io Continuous Monitoring.
type Enroller struct {
	APIKey      string // from Secrets Manager
	WebhookURL  string // One Fintech-hosted webhook endpoint registered with sanctions.io
}

// Enroll registers a confirmed member with sanctions.io after RT30 completion.
// Called once per member per Stage 7 RT30 COMPLETED confirmation.
func (e *Enroller) Enroll(ctx context.Context, clientMemberID, firstName, lastName, dob string) error {
	// TODO: POST to sanctions.io enrollment API
	// TODO: register WebhookURL for this member's alerts
	return nil
}
