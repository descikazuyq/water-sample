package water

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时的状态完整性校验：每份已保存样品都必须明确写
// 有三种状态之一（pending、confirmed、voided）。status 字段缺失、为 null、
// 为空字符串，或写成任何其他字符串（含带首尾空白、大小写不同的写法）都是
// 损坏记录，整次 Open 以 ErrCorruptRecord 失败，返回 nil 存放，错误信息点
// 到具体样品编号并区分状态缺失与不支持的状态值；原测量完整、逐项判定齐全
// 或作废原因仍在都不能代替明确的状态，也不能据此猜出它原来是否作废。同一
// 文件中其他样品正常也不只读入正常部分；打开失败不改写原文件、不补状态。

// 状态字段整个缺失的样品（其余内容完整，甚至保留了作废原因）必须整次拒绝：
// 不能凭“样品污染”的作废原因猜出它原来是 voided。
func TestOpenMissingStatusField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "voidReason": "样品污染"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejects(t, dir, "S1", "", true)
}

// 状态保存为 JSON null：与字段缺失一样是状态缺失，整次打开失败。
func TestOpenNullStatus(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}], "status": null}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejects(t, dir, "S1", "", true)
}

// 状态为空字符串同样是状态缺失，不能当成待判定接受。
func TestOpenEmptyStatus(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       "",
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 状态缺失但逐项判定完整且自洽：即使记录保留了完整逐项判定，也不能据此
// 猜出它原来是已确认还是已作废，整次打开失败。
func TestOpenMissingStatusWithCompleteResults(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Results:      phTurbResults(),
				Exceeded:     true,
				Status:       "",
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 未支持的状态字符串：错误信息中原样带出未知字符串，不能自动整理后接受。
func TestOpenUnknownStatus(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       "archived",
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "archived", false)
}

// 带首尾空白、大小写不同的写法都不合法：不能去掉空白或统一大小写后接受。
func TestOpenStatusNotNormalized(t *testing.T) {
	for _, status := range []Status{" pending", "pending ", "Pending", "CONFIRMED", " Voided "} {
		t.Run(string(status), func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": {
						ID: "S1", PointID: "P1", SampledAt: at(10, 0),
						Measurements: []Measurement{{Item: openPH, Value: 7}},
						Status:       status,
					},
				},
			})
			assertOpenRejects(t, dir, "S1", string(status), false)
		})
	}
}

// 同一文件中其他样品完全正常，也不能只读入正常部分：一份状态损坏即整次失败。
func TestOpenCorruptStatusRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			"S2": {
				ID: "S2", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       "",
			},
		},
	})
	assertOpenRejects(t, dir, "S2", "", true)
}

// 三种合法状态原样读入：待判定可确认，确认后作废的样品保留原测量、原判定
// 与原因，不能再次确认，也不成为最近有效结果。
func TestOpenLegalStatusesUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {{PointID: "P1", Item: openPH, Value: 8, Effective: openEff()}},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			"S2": {
				ID: "S2", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
				Exceeded: true,
			},
			"S3": {
				ID: "S3", PointID: "P1", SampledAt: at(12, 0),
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusVoided, VoidReason: "样品污染",
			},
			"S4": {
				ID: "S4", PointID: "P1", SampledAt: at(13, 0),
				Measurements: []Measurement{{Item: openPH, Value: 5}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染",
				Results: []ItemResult{
					{Item: openPH, Value: 5, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
				Exceeded: false,
			},
		},
	})
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("合法状态应正常打开: %v", err)
	}
	defer st.Close()
	// 确认后作废的样品不能再次确认。
	if _, err := st.Confirm("S4"); !errors.Is(err, ErrVoided) {
		t.Fatalf("确认后作废的样品不能再确认，got %v", err)
	}
	// 最近有效结果跳过作废与待判定，取 S2。
	latest, ok, err := st.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S2" {
		t.Fatalf("最近有效结果应为 S2，got %v, ok=%v, err=%v", latest.ID, ok, err)
	}
	// 待判定样品照常确认。
	smp, err := st.Confirm("S1")
	if err != nil || smp.Status != StatusConfirmed {
		t.Fatalf("待判定样品应可确认，got %v, %v", smp.Status, err)
	}
}
