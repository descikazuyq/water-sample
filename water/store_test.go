package water

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func tz(h int) time.Time {
	return time.Date(2026, 1, 10, 12, 0, 0, 0, time.FixedZone("X", h*3600))
}

func TestReadyStillWorks(t *testing.T) {
	if !Ready() {
		t.Fatal("Ready broken")
	}
}

func TestRegisterPoint(t *testing.T) {
	s := openTestStore(t)

	if err := s.RegisterPoint(" P1 ", " 采样点一 "); err != nil {
		t.Fatal(err)
	}
	// 重复编号拒绝
	if err := s.RegisterPoint("P1", "别的名字"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("want duplicate, got %v", err)
	}
	// 编号/名称为空拒绝
	if err := s.RegisterPoint("  ", "x"); !errors.Is(err, ErrInvalidNumber) {
		t.Fatalf("want invalid number, got %v", err)
	}
	if err := s.RegisterPoint("P2", "  "); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("want invalid name, got %v", err)
	}

	pt, err := s.GetPoint("P1")
	if err != nil {
		t.Fatal(err)
	}
	if pt.Number != "P1" || pt.Name != "采样点一" {
		t.Fatalf("trim not applied: %+v", pt)
	}
	pts, err := s.ListPoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("want 1 point, got %d", len(pts))
	}
}

func TestSetLimit(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}

	t1 := tz(0)
	t1OtherZone := t1.In(time.FixedZone("Y", 5*3600)) // 同一时刻，不同时区
	if err := s.SetLimit("P1", "COD", 10, t1); err != nil {
		t.Fatal(err)
	}
	// 同一时刻（不同时区）的第二版必须拒绝
	if err := s.SetLimit("P1", "COD", 20, t1OtherZone); !errors.Is(err, ErrDuplicateLimit) {
		t.Fatalf("want duplicate limit, got %v", err)
	}
	// 乱序补录允许
	t0 := t1.Add(-time.Hour)
	t2 := t1.Add(time.Hour)
	if err := s.SetLimit("P1", "COD", 8, t2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimit("P1", "COD", 6, t0); err != nil {
		t.Fatal(err)
	}
	// 校验
	if err := s.SetLimit("P1", "COD", math.NaN(), t1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("want invalid limit, got %v", err)
	}
	if err := s.SetLimit("P1", "COD", math.Inf(1), t1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("want invalid limit, got %v", err)
	}
	if err := s.SetLimit("P1", "COD", 5, time.Time{}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("want invalid time, got %v", err)
	}
	if err := s.SetLimit("NOPE", "COD", 5, t1); !errors.Is(err, ErrPointNotFound) {
		t.Fatalf("want point not found, got %v", err)
	}

	limits, err := s.ListLimits("P1", "COD")
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 3 || limits[0].Limit != 8 || limits[1].Limit != 10 || limits[2].Limit != 6 {
		t.Fatalf("limits order/content wrong: %+v", limits)
	}
}

func TestSubmitSampleValidation(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	sampled := tz(0)

	// 缺项目
	if _, err := s.SubmitSample("S1", "P1", sampled, nil); !errors.Is(err, ErrNoMeasurements) {
		t.Fatalf("want no measurements, got %v", err)
	}
	// 重复项目
	_, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "COD", Value: 1}, {Item: " COD ", Value: 2},
	})
	if !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("want duplicate item, got %v", err)
	}
	// 非有限数
	if _, err := s.SubmitSample("S1", "P1", sampled, []Measurement{{Item: "COD", Value: math.NaN()}}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("want invalid value, got %v", err)
	}
	// 缺采样时间
	if _, err := s.SubmitSample("S1", "P1", time.Time{}, []Measurement{{Item: "COD", Value: 1}}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("want invalid time, got %v", err)
	}
	// 不存在的采样点
	if _, err := s.SubmitSample("S1", "NOPE", sampled, []Measurement{{Item: "COD", Value: 1}}); !errors.Is(err, ErrPointNotFound) {
		t.Fatalf("want point not found, got %v", err)
	}

	// 以上拒绝都不得留下样品
	if _, err := s.GetSample("S1"); !errors.Is(err, ErrSampleNotFound) {
		t.Fatalf("rejected submission left a sample: %v", err)
	}
}

func TestSubmitSampleDuplicateRules(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	sampled := tz(0)

	orig, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "COD", Value: 5}, {Item: "NH3", Value: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if orig.State != StatePending {
		t.Fatalf("new sample should be pending, got %s", orig.State)
	}

	// 同内容、不同顺序 → 返回原样品
	again, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "NH3", Value: 1}, {Item: "COD", Value: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Number != orig.Number || again.State != StatePending {
		t.Fatalf("same content should return original: %+v", again)
	}

	// 任一内容不同 → 拒绝
	if _, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "COD", Value: 6}, {Item: "NH3", Value: 1},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict on changed value, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "COD", Value: 5},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict on removed item, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", sampled.Add(time.Second), []Measurement{
		{Item: "COD", Value: 5}, {Item: "NH3", Value: 1},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict on changed sample time, got %v", err)
	}

	// 拒绝后原样品不变
	got, err := s.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Measurements) != 2 || got.Measurements[0].Value != 5 {
		t.Fatalf("original sample mutated: %+v", got)
	}
}

func TestConfirmSample(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	t0 := tz(0)
	sampled := t0.Add(2 * time.Hour)

	// COD 有两版限值：t0 生效的 10，t0+1h 生效的 5；采样时刻应选 5
	if err := s.SetLimit("P1", "COD", 10, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimit("P1", "COD", 5, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// NH3 没有限值
	if _, err := s.SubmitSample("S1", "P1", sampled, []Measurement{
		{Item: "COD", Value: 5}, // 恰好等于上限 → 达标
		{Item: "NH3", Value: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// 缺限值 → 拒绝，保持待判定，无部分结论
	if _, err := s.ConfirmSample("S1"); !errors.Is(err, ErrNoApplicableLimit) {
		t.Fatalf("want no applicable limit, got %v", err)
	}
	got, err := s.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePending || got.Results != nil {
		t.Fatalf("sample should stay pending without results: %+v", got)
	}

	// 补齐限值后可确认
	if err := s.SetLimit("P1", "NH3", 1, t0); err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.ConfirmSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.State != StateConfirmed || confirmed.Exceeded {
		t.Fatalf("COD==limit should pass: %+v", confirmed)
	}
	if len(confirmed.Results) != 2 {
		t.Fatalf("want 2 item results, got %d", len(confirmed.Results))
	}
	var cod *ItemResult
	for i := range confirmed.Results {
		if confirmed.Results[i].Item == "COD" {
			cod = &confirmed.Results[i]
		}
	}
	if cod == nil || cod.Limit != 5 || !cod.EffectiveAt.Equal(t0.Add(time.Hour)) || cod.Exceeded {
		t.Fatalf("COD should use newest limit 5: %+v", cod)
	}

	// 重复确认直接返回已保存结果
	again, err := s.ConfirmSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != StateConfirmed || !again.ConfirmedAt.Equal(confirmed.ConfirmedAt) {
		t.Fatalf("repeat confirm should return saved result")
	}

	// 确认后新增/补录限值不改变已确认结果
	if err := s.SetLimit("P1", "COD", 1, t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetSample("S1")
	if got2.Results[0].Limit != 5 && got2.Results[1].Limit != 5 {
		t.Fatalf("confirmed results changed after new limit: %+v", got2.Results)
	}
}

func TestConfirmExceedingAndBoundary(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	t0 := tz(0)
	if err := s.SetLimit("P1", "COD", 10, t0); err != nil {
		t.Fatal(err)
	}

	// 采样时间恰好等于生效时间 → 使用新值（10）
	if _, err := s.SubmitSample("EQ", "P1", t0, []Measurement{{Item: "COD", Value: 10}}); err != nil {
		t.Fatal(err)
	}
	c, err := s.ConfirmSample("EQ")
	if err != nil || c.Exceeded {
		t.Fatalf("equal at effective time should pass: %v %+v", err, c)
	}

	// 大于上限 → 超标
	if _, err := s.SubmitSample("OVER", "P1", t0.Add(time.Hour), []Measurement{{Item: "COD", Value: 10.01}}); err != nil {
		t.Fatal(err)
	}
	c2, err := s.ConfirmSample("OVER")
	if err != nil || !c2.Exceeded {
		t.Fatalf("10.01 > 10 should exceed: %v %+v", err, c2)
	}

	// 早于生效时间采样 → 无适用上限
	if _, err := s.SubmitSample("EARLY", "P1", t0.Add(-time.Hour), []Measurement{{Item: "COD", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("EARLY"); !errors.Is(err, ErrNoApplicableLimit) {
		t.Fatalf("sample before effective time should have no limit, got %v", err)
	}
}

func TestVoidSample(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	t0 := tz(0)
	if err := s.SetLimit("P1", "COD", 10, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSample("S1", "P1", t0.Add(time.Hour), []Measurement{{Item: "COD", Value: 5}}); err != nil {
		t.Fatal(err)
	}

	// 空原因拒绝
	if err := s.VoidSample("S1", "   "); !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("want invalid reason, got %v", err)
	}
	// 待判定样品作废
	if err := s.VoidSample("S1", "  容器破损  "); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateVoided || v.Reason != "容器破损" {
		t.Fatalf("void not applied: %+v", v)
	}
	// 作废后不能确认
	if _, err := s.ConfirmSample("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("voided sample must not be confirmable, got %v", err)
	}
	// 重复提交相同（处理后）原因 → 成功；不同原因 → 拒绝
	if err := s.VoidSample("S1", "容器破损"); err != nil {
		t.Fatalf("same reason should be idempotent: %v", err)
	}
	if err := s.VoidSample("S1", "别的原因"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict on different reason, got %v", err)
	}

	// 已确认样品也能作废，且保留结果
	if _, err := s.SubmitSample("S2", "P1", t0.Add(2*time.Hour), []Measurement{{Item: "COD", Value: 20}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("S2"); err != nil {
		t.Fatal(err)
	}
	if err := s.VoidSample("S2", "重测"); err != nil {
		t.Fatal(err)
	}
	v2, _ := s.GetSample("S2")
	if v2.State != StateVoided || v2.Results == nil || !v2.Exceeded {
		t.Fatalf("voided confirmed sample should keep results: %+v", v2)
	}
}

func TestListSamplesAndLatest(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	t0 := tz(0)
	if err := s.SetLimit("P1", "COD", 10, t0); err != nil {
		t.Fatal(err)
	}

	mk := func(n string, ts time.Time, val float64) {
		t.Helper()
		if _, err := s.SubmitSample(n, "P1", ts, []Measurement{{Item: "COD", Value: val}}); err != nil {
			t.Fatal(err)
		}
	}
	// 同一时刻两个样品，编号升序：S-A、S-B
	mk("S-B", t0.Add(2*time.Hour), 5) // 待判定
	mk("S-A", t0.Add(2*time.Hour), 5) // 将确认
	mk("S-C", t0.Add(3*time.Hour), 5) // 将作废
	mk("S-D", t0.Add(4*time.Hour), 5) // 已确认，最晚

	if _, err := s.ConfirmSample("S-A"); err != nil {
		t.Fatal(err)
	}
	if err := s.VoidSample("S-C", "作废"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("S-D"); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListSamples("P1")
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"S-D", "S-C", "S-A", "S-B"}
	if len(list) != 4 {
		t.Fatalf("want 4 samples, got %d", len(list))
	}
	for i, w := range wantOrder {
		if list[i].Number != w {
			t.Fatalf("position %d: want %s, got %s", i, w, list[i].Number)
		}
	}
	// 状态区分
	if list[0].State != StateConfirmed || list[1].State != StateVoided ||
		list[2].State != StateConfirmed || list[3].State != StatePending {
		t.Fatalf("states wrong: %+v", list)
	}

	// 最近有效结果：S-D 最晚且已确认未作废
	latest, err := s.LatestResult("P1")
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Sample.Number != "S-D" {
		t.Fatalf("want latest S-D, got %+v", latest)
	}

	// 没有已确认样品时明确无结果
	if err := s.RegisterPoint("P2", "点二"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSample("P2-S1", "P2", t0, []Measurement{{Item: "X", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	none, err := s.LatestResult("P2")
	if err != nil {
		t.Fatal(err)
	}
	if none.Found {
		t.Fatalf("want no result, got %+v", none)
	}
}

func TestPersistenceAndIsolation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimit("P1", "COD", 10, tz(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSample("S1", "P1", tz(0).Add(time.Hour), []Measurement{{Item: "COD", Value: 5}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("S1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 重新打开，记录仍在
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateConfirmed || !got.Exceeded && got.Results == nil {
		t.Fatalf("persisted record wrong: %+v", got)
	}
	latest, err := s2.LatestResult("P1")
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Sample.Number != "S1" {
		t.Fatalf("persisted latest wrong: %+v", latest)
	}

	// 不同目录互不混用
	other, err := Open(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.GetSample("S1"); !errors.Is(err, ErrSampleNotFound) {
		t.Fatalf("stores leaked across directories: %v", err)
	}
}

func TestRejectedOperationsLeaveNoTraceOnReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	// 确认一个缺限值的样品 → 拒绝；作废一个不存在的样品 → 拒绝
	if _, err := s.SubmitSample("S1", "P1", tz(0), []Measurement{{Item: "COD", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("S1"); !errors.Is(err, ErrNoApplicableLimit) {
		t.Fatal(err)
	}
	if err := s.VoidSample("NOPE", "原因"); !errors.Is(err, ErrSampleNotFound) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePending || got.Results != nil {
		t.Fatalf("rejected confirm left trace after reopen: %+v", got)
	}
}

func TestResubmitConfirmedAndVoidedSamples(t *testing.T) {
	s := openTestStore(t)
	if err := s.RegisterPoint("P1", "点一"); err != nil {
		t.Fatal(err)
	}
	t0 := tz(0)
	if err := s.SetLimit("P1", "COD", 10, t0); err != nil {
		t.Fatal(err)
	}

	// 已确认样品：不同内容拒绝，相同内容返回原样品
	if _, err := s.SubmitSample("S1", "P1", t0.Add(time.Hour), []Measurement{{Item: "COD", Value: 5}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSample("S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSample("S1", "P1", t0.Add(time.Hour), []Measurement{{Item: "COD", Value: 6}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirmed sample: want conflict, got %v", err)
	}
	same, err := s.SubmitSample("S1", "P1", t0.Add(time.Hour), []Measurement{{Item: "COD", Value: 5}})
	if err != nil || same.State != StateConfirmed {
		t.Fatalf("confirmed sample same content should return original: %v %+v", err, same)
	}

	// 已作废样品：不同内容同样拒绝
	if _, err := s.SubmitSample("S2", "P1", t0.Add(2*time.Hour), []Measurement{{Item: "COD", Value: 5}}); err != nil {
		t.Fatal(err)
	}
	if err := s.VoidSample("S2", "作废"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitSample("S2", "P1", t0.Add(2*time.Hour), []Measurement{{Item: "COD", Value: 9}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("voided sample: want conflict, got %v", err)
	}
}
