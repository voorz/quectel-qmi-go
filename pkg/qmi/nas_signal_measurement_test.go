package qmi

import "testing"

// 射频刚从 RFOff 恢复时，NAS Get Signal Strength 会"成功"返回 -125 哨兵且不带任何
// 伴随字段。把它当读数缓存下来，界面会长期显示无信号——快照的 RSSI 只由显式查询写入，
// 一旦写进假值可能长期没机会被覆盖。
func TestSignalStrengthHasMeasurement(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  *SignalStrength
		want bool
	}{
		{"nil", nil, false},
		{"哨兵 RSSI 且无伴随字段 = 尚无测量", &SignalStrength{RSSI: NoMeasurementRSSIdBm}, false},
		{"正常读数", &SignalStrength{RSSI: -55, RSRP: -83, RSRQ: -9, SNR: 120}, true},
		{"仅 RSSI 的正常读数", &SignalStrength{RSSI: -95}, true},
		// 真实的极弱信号：RSSI 恰好落在 -125，但 LTE 字段有值，说明模组确实测到了。
		{"哨兵 RSSI 但带 RSRP", &SignalStrength{RSSI: NoMeasurementRSSIdBm, RSRP: -120}, true},
		{"哨兵 RSSI 但带 RSRQ", &SignalStrength{RSSI: NoMeasurementRSSIdBm, RSRQ: -19}, true},
		{"哨兵 RSSI 但带 SNR", &SignalStrength{RSSI: NoMeasurementRSSIdBm, SNR: -20}, true},
		// 零值 RSSI 不是哨兵，交给上层按"未知"处理，这里不越权判定。
		{"零值 RSSI", &SignalStrength{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sig.HasMeasurement(); got != tc.want {
				t.Fatalf("HasMeasurement() = %v, want %v", got, tc.want)
			}
		})
	}
}
