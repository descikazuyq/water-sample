package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 限值只能属于登记它的（采样点，项目）：两个字段各自相同才算同一组。
// 编号与项目名内部允许包含 U+0000，不能用单字符拼接键把不同组合撞成一组。
func TestLimitGroupOwnership(t *testing.T) {
	s, _ := open(t)
	zeroPoint := "P\x00A" // 〈零〉在编号中间
	zeroItem := "A\x00B"  // 〈零〉在项目名中间
	mustPoint(t, s, zeroPoint, "带零编号")
	mustPoint(t, s, "P", "普通编号")

	eff := at(1, 0)
	// 两组在同一时刻分别登记上限 10 和 5，互不报生效时间重复
	mustLimit(t, s, zeroPoint, "B", 10, eff)
	mustLimit(t, s, "P", zeroItem, 5, eff)

	sampled := at(2, 0)
	mustSample(t, s, "SZ", zeroPoint, sampled, Measurement{Item: "B", Value: 7})
	mustSample(t, s, "SN", "P", sampled, Measurement{Item: zeroItem, Value: 7})

	rz, err := s.Confirm("SZ")
	if err != nil {
		t.Fatalf("Confirm SZ: %v", err)
	}
	if rz.Exceeded || len(rz.Results) != 1 {
		t.Fatalf("7 <= 10 应达标: %+v", rz)
	}
	if r := rz.Results[0]; r.Item != "B" || r.Value != 7 || r.Limit != 10 ||
		!r.LimitEffective.Equal(eff) || r.Exceeded {
		t.Fatalf("前者判定依据错误: %+v", r)
	}

	rn, err := s.Confirm("SN")
	if err != nil {
		t.Fatalf("Confirm SN: %v", err)
	}
	if !rn.Exceeded || len(rn.Results) != 1 {
		t.Fatalf("7 > 5 应超标: %+v", rn)
	}
	if r := rn.Results[0]; r.Item != zeroItem || r.Value != 7 || r.Limit != 5 ||
		!r.LimitEffective.Equal(eff) || !r.Exceeded {
		t.Fatalf("后者判定依据错误: %+v", r)
	}

	// 按采样点查看：各自只看到本组样品与本组判定依据
	for _, c := range []struct {
		point string
		id    string
		lim   float64
		item  string
	}{
		{zeroPoint, "SZ", 10, "B"},
		{"P", "SN", 5, zeroItem},
	} {
		list, err := s.ListByPoint(c.point)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListByPoint(%q): %+v err=%v", c.point, list, err)
		}
		got := list[0]
		if got.ID != c.id || len(got.Results) != 1 {
			t.Fatalf("按点查看串组: %+v", got)
		}
		if r := got.Results[0]; r.Item != c.item || r.Limit != c.lim ||
			!r.LimitEffective.Equal(eff) {
			t.Fatalf("按点查看的判定依据错误: %+v", r)
		}
	}
}

// 只登记了一组限值时，另一组的样品必须因缺适用上限拒绝确认：
// 保持待判定、不留部分结果、不借用另一组的限值。
func TestLimitGroupMissingLimit(t *testing.T) {
	s, _ := open(t)
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	mustPoint(t, s, zeroPoint, "带零编号")
	mustPoint(t, s, "P", "普通编号")
	mustLimit(t, s, zeroPoint, "B", 10, at(1, 0))
	// "P"/"A\x00B" 没有登记任何限值

	mustSample(t, s, "SN", "P", at(2, 0), Measurement{Item: zeroItem, Value: 7})
	if _, err := s.Confirm("SN"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("另一组缺上限应拒绝，got %v", err)
	}
	list, err := s.ListByPoint("P")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if got := list[0]; got.Status != StatusPending || got.Results != nil || got.Exceeded {
		t.Fatalf("拒绝后必须保持待判定且无部分结果: %+v", got)
	}
	if _, ok, err := s.LatestResult("P"); err != nil || ok {
		t.Fatalf("另一组不应出现已确认结果 ok=%v err=%v", ok, err)
	}

	// 被拒绝的确认不影响已有组的判定
	mustSample(t, s, "SZ", zeroPoint, at(2, 0), Measurement{Item: "B", Value: 7})
	rz, err := s.Confirm("SZ")
	if err != nil || rz.Exceeded || rz.Results[0].Limit != 10 {
		t.Fatalf("已有组判定受影响: %+v err=%v", rz, err)
	}
}

// 两组各有多版限值时，独立选择采样当时已生效的最近一版；
// 另一组更晚生效的版本不影响本组；边界时刻与等于上限的规则保持不变。
func TestLimitGroupVersionsIndependent(t *testing.T) {
	s, _ := open(t)
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	mustPoint(t, s, zeroPoint, "带零编号")
	mustPoint(t, s, "P", "普通编号")

	// zeroPoint/B：10@9/1、20@9/10
	mustLimit(t, s, zeroPoint, "B", 10, at(1, 0))
	mustLimit(t, s, zeroPoint, "B", 20, at(10, 0))
	// P/zeroItem：5@9/1、50@9/20（另一组更晚的 20@9/10 与本组互不干涉）
	mustLimit(t, s, "P", zeroItem, 5, at(1, 0))
	mustLimit(t, s, "P", zeroItem, 50, at(20, 0))

	// 9/10 采样：zeroPoint 组恰好生效 20（边界用新版，等于上限达标）
	mustSample(t, s, "SZ", zeroPoint, at(10, 0), Measurement{Item: "B", Value: 20})
	rz, err := s.Confirm("SZ")
	if err != nil {
		t.Fatalf("Confirm SZ: %v", err)
	}
	if rz.Exceeded || rz.Results[0].Limit != 20 ||
		!rz.Results[0].LimitEffective.Equal(at(10, 0)) {
		t.Fatalf("边界时刻应用该版且等于上限达标: %+v", rz.Results[0])
	}

	// 9/15 采样：P 组只能用 5（本组的 50 尚未生效，另一组的 20 不能借用）
	mustSample(t, s, "SN", "P", at(15, 0), Measurement{Item: zeroItem, Value: 7})
	rn, err := s.Confirm("SN")
	if err != nil {
		t.Fatalf("Confirm SN: %v", err)
	}
	if !rn.Exceeded || rn.Results[0].Limit != 5 ||
		!rn.Results[0].LimitEffective.Equal(at(1, 0)) {
		t.Fatalf("本组不能受另一组版本影响: %+v", rn.Results[0])
	}

	// 同组同一时刻（不同时区表示）仍报重复，且不覆盖原值
	cst5 := time.Date(2026, 9, 10, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if _, err := s.SetLimit(zeroPoint, "B", 99, cst5); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("同组同一时刻换时区表示应报重复, got %v", err)
	}
	mustSample(t, s, "SZ2", zeroPoint, at(11, 0), Measurement{Item: "B", Value: 25})
	rz2, err := s.Confirm("SZ2")
	if err != nil || rz2.Results[0].Limit != 20 || !rz2.Exceeded {
		t.Fatalf("被拒绝的登记不得覆盖原值: %+v err=%v", rz2.Results[0], err)
	}
	// 另一组记录也不受这次拒绝影响
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 2 {
		t.Fatalf("拒绝操作改动了另一组记录: %+v", got)
	}
}

// 旧版本曾把限值按 "pointID + U+0000 + item" 单键落盘，
// 包含 U+0000 的不同组合会共用同一个键。重新打开旧文件时必须按每条
// 限值自身的采样点与项目重新分组，已确认样品的原结论与所用限值保持不变。
func TestLimitGroupReloadOldCollidedFile(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	eff := at(1, 0)

	// 手工构造旧格式文件：两组限值落在同一个碰撞键下，
	// 但每条记录各自带着正确的 pointId/item。
	oldKey := zeroPoint + "\x00" + "B" // 旧算法的键，与 "P" + "\x00" + zeroItem 相同
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("test setup: 两个组合应拼出同一个旧键")
	}
	old := diskState{
		Points: map[string]SamplingPoint{
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
			"P":       {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: zeroPoint, Item: "B", Value: 10, Effective: eff},
				{PointID: "P", Item: zeroItem, Value: 5, Effective: eff},
			},
		},
		Samples: map[string]*Sample{
			"SN": {
				ID: "SN", PointID: "P", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: zeroItem, Value: 7}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{{
					Item: zeroItem, Value: 7, Limit: 5,
					LimitEffective: eff, Exceeded: true,
				}},
			},
		},
	}
	data, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatalf("marshal old file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), data, 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open old file: %v", err)
	}
	defer s.Close()

	if got := s.limits[limitGroup{pointID: zeroPoint, item: "B"}]; len(got) != 1 || got[0].Value != 10 {
		t.Fatalf("旧文件重新分组失败（zeroPoint 组）: %+v", got)
	}
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 1 || got[0].Value != 5 {
		t.Fatalf("旧文件重新分组失败（P 组）: %+v", got)
	}

	// 已确认样品保留原结论与所用限值，不自动重新判定
	kept, err := s.Confirm("SN")
	if err != nil {
		t.Fatalf("Confirm kept sample: %v", err)
	}
	if r := kept.Results[0]; !kept.Exceeded || r.Item != zeroItem ||
		r.Value != 7 || r.Limit != 5 || !r.LimitEffective.Equal(eff) {
		t.Fatalf("已确认样品结论被改动: %+v", kept)
	}

	// 同一时刻两组各自再登记仍互不冲突；同组重复仍拒绝
	if _, err := s.SetLimit(zeroPoint, "B", 11, at(5, 0)); err != nil {
		t.Fatalf("两组独立补录应成功: %v", err)
	}
	if _, err := s.SetLimit("P", zeroItem, 6, at(5, 0)); err != nil {
		t.Fatalf("两组独立补录应成功: %v", err)
	}
	if _, err := s.SetLimit(zeroPoint, "B", 12, eff); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("同组同一时刻仍应报重复, got %v", err)
	}

	// 再次落盘并重新打开，归属仍然正确
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.limits[limitGroup{pointID: zeroPoint, item: "B"}]; len(got) != 2 {
		t.Fatalf("重开后版本丢失或串组: %+v", got)
	}
	if got := s2.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 2 {
		t.Fatalf("重开后版本丢失或串组: %+v", got)
	}
}
