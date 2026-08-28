package main

import (
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/manager"
)

func TestDataPlaneSpecFromFlag(t *testing.T) {
	tests := []struct {
		name  string
		muxID uint8
		want  manager.DataPlaneSpec
	}{
		{name: "native", muxID: 0, want: manager.DataPlaneSpec{Mode: manager.DataPlaneModeNative}},
		{name: "qmap", muxID: 1, want: manager.DataPlaneSpec{Mode: manager.DataPlaneModeQMAP, DefaultMuxID: 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dataPlaneSpecFromFlag(test.muxID); got != test.want {
				t.Fatalf("dataPlaneSpecFromFlag(%d) = %+v, want %+v", test.muxID, got, test.want)
			}
		})
	}
}
