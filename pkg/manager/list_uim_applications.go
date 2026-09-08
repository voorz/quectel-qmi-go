package manager

import (
	"context"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

// ListUIMApplications returns the complete application list from UIM card
// status, including each application's state and full AID.
func (m *Manager) ListUIMApplications(ctx context.Context) ([]qmi.UIMApplication, error) {
	return withCardAccessValue(m, ctx, func() ([]qmi.UIMApplication, error) {
		return withUIMRecoveryValue(m, "ListUIMApplications", func(uim *qmi.UIMService) ([]qmi.UIMApplication, error) {
			return uim.ListApplications(ctx)
		})
	})
}
