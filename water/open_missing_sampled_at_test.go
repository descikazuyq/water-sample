package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时的采样时间完整性校验：每份样品都必须保留采样
// 时间。sampledAt 字段缺失、保存为 null，或明确保存为 Go 零时间
// 0001-01-01T00:00:00Z，都视为缺少采样时间，整次 Open 以 ErrCorruptRecord
// 失败，返回 nil 存放，错误信息点到具体样品编号并说明缺少的是样品的采样时间
// （与登记限值或判定依据缺少生效时间区分开）。这条要求适用于待判定、已确认
// 和已作废的全部样品：测量项目与数值完整、逐项判定相互一致都不能代替日期；
// 同一文件中其他样品正常也不只读入正常部分；拒绝读取时保留原文件，不补成
// 当前时间、不从所用上限的生效时间推测采样日期、不改变样品状态或已有结论。

// assertOpenRejectsSampledAt 断言打开整次失败：返回 nil 存放、错误可识别为
// ErrCorruptRecord，信息指出样品编号并说明缺少的是采样时间（不是上限生效
// 时间），原文件字节不被改动。
func assertOpenRejectsSampledAt(t *testing.T, dir, sampleID string) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("缺少采样时间的样品必须让整次 Open 失败，sample=%s", sampleID)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出样品编号 %q，实际 %q", sampleID, msg)
	}
	if !strings.Contains(msg, "缺少采样时间") {
		t.Fatalf("错误信息应说明缺少的是采样时间，实际 %q", msg)
	}
	if strings.Contains(msg, "采样时该上限尚未生效") {
		t.Fatalf("缺少采样时间不应只报成上限生效时间晚于采样时间，实际 %q", msg)
	}
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file after open: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("打开失败不得改写原文件")
	}
}

// sampledAt 字段整个缺失的待判定样品（其余内容完整）必须整次拒绝。
func TestOpenMissingSampledAtField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// sampledAt 保存为 JSON null：与字段缺失一样是缺少采样时间，整次打开失败。
func TestOpenNullSampledAt(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": null,
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// sampledAt 明确保存为 Go 零时间 0001-01-01T00:00:00Z：同样视为缺少采样时间，
// 不能当成一个很早的真实采样时刻接受。
func TestOpenZeroSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// 已确认样品缺采样时间：逐项判定完整且自洽也不能代替日期；错误必须明确报告
// 缺少采样时间，而不是只报某项上限生效时间晚于采样时间。
func TestOpenConfirmedMissingSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH):   {{PointID: "P1", Item: openPH, Value: 8, Effective: openEff()}},
			diskLimitKey("P1", openTurb): {{PointID: "P1", Item: openTurb, Value: 4, Effective: openEff()}},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: phTurbMeasurements(),
				Results:      phTurbResults(),
				Exceeded:     true,
				Status:       StatusConfirmed,
			},
		},
	})
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// 已确认后作废的样品缺采样时间：作废不取消采样时间必须存在的要求。
func TestOpenVoidedAfterConfirmMissingSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: phTurbMeasurements(),
				Results:      phTurbResults(),
				Exceeded:     true,
				Status:       StatusVoided, VoidReason: "复测确认样品污染",
			},
		},
	})
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// 未确认就作废的样品缺采样时间：没有逐项判定依据也不能放过丢失的日期。
func TestOpenVoidedPendingMissingSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusVoided, VoidReason: "样品污染",
			},
		},
	})
	assertOpenRejectsSampledAt(t, dir, "S1")
}

// 同一文件中其他样品完全正常，也不能只读入正常部分：一份缺采样时间即整次失败。
func TestOpenMissingSampledAtRejectsWholeFile(t *testing.T) {
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
				ID: "S2", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsSampledAt(t, dir, "S2")
}
