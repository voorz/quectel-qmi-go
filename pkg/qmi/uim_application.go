package qmi

import (
	"context"
	"fmt"
)

// UIMApplication is one application reported by UIM_GET_CARD_STATUS.
// AID is the complete application identifier, not only the well-known prefix.
type UIMApplication struct {
	Type  uint8
	State uint8
	AID   []byte
}

// getCardStatusValue sends UIM_GET_CARD_STATUS and returns the raw TLV 0x10
// value. Shared by GetCardStatusDetails, getCardStatusAID, and ListApplications
// so they all go through one path instead of duplicating the request+parse.
func (u *UIMService) getCardStatusValue(ctx context.Context) ([]byte, error) {
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetCardStatus, nil)
	if err != nil {
		return nil, err
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("UIM get card status failed: %w", err)
	}
	tlv := FindTLV(resp.TLVs, 0x10)
	if tlv == nil || len(tlv.Value) < 15 {
		return nil, fmt.Errorf("card status TLV missing or too short")
	}
	return append([]byte(nil), tlv.Value...), nil
}

// ListApplications returns every application from the active UIM card status.
// The returned AIDs are complete and each item preserves its application state.
func (u *UIMService) ListApplications(ctx context.Context) ([]UIMApplication, error) {
	v, err := u.getCardStatusValue(ctx)
	if err != nil {
		return nil, err
	}
	raw := parseUIMCardStatusApps(v, v[14])
	apps := make([]UIMApplication, 0, len(raw))
	for _, app := range raw {
		apps = append(apps, UIMApplication{
			Type:  app.appType,
			State: app.appState,
			AID:   append([]byte(nil), app.aid...),
		})
	}
	return apps, nil
}
