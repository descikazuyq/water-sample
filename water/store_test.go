package water

import (
	"errors"
	"math"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func mustPoint(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if _, err := s.RegisterPoint(id, name); err != nil {
		t.Fatalf("RegisterPoint(%q): %v", id, err)
	}
}

func mustLimit(t *testing.T, s *Store, point, item string, v float64, eff time.Time) {
	t.Helper()
	if _, err := s.SetLimit(point, item, v, eff); err != nil {
		t.Fatalf("SetLimit(%s/%s): %v", point, item, err)
	}
}

func mustSample(t *testing.T, s *Store, id, point string, at time.Time, ms ...Measurement) Sample {
	t.Helper()
	smp, err := s.SubmitSample(id, point, at, ms...)
	if err != nil {
		t.Fatalf("SubmitSample(%q): %v", id, err)
	}
	return smp
}

func at(day, hour int) time.Time {
	return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC)
}

func TestRegisterPoint(t *testing.T) {
	s, _ := open(t)
	p, err := s.RegisterPoint("  P1 ", "  取水口  ")
	if err != nil {
		t.Fatalf("RegisterPoint: %v", err)
	}
	if p.ID != "P1" || p.Name != "取水口" {
		t.Fatalf("expected trimmed text, got %+v", p)
	}
	if _, err := s.RegisterPoint("P1", "另一个名字"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("duplicate id should be rejected, got %v", err)
	}
	for _, id := range []string{"", "   "} {
		if _, err := s.RegisterPoint(id, "x"); !errors.Is(err, ErrEmptyField) {
			t.Fatalf("empty id should be rejected, got %v", err)
		}
	}
	if _, err := s.RegisterPoint("P2", "  "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("empty name should be rejected, got %v", err)
	}
}

func TestSetLimit(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	if _, err := s.SetLimit("P9", "pH", 7, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("unknown point, got %v", err)
	}
	if _, err := s.SetLimit("P1", " ", 7, at(1, 0)); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("blank item, got %v", err)
	}
	if _, err := s.SetLimit("P1", "pH", math.NaN(), at(1, 0)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("NaN, got %v", err)
	}
	if _, err := s.SetLimit("P1", "pH", math.Inf(1), at(1, 0)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("+Inf, got %v", err)
	}
	if _, err := s.SetLimit("P1", "pH", 7, time.Time{}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("zero time, got %v", err)
	}

	// 乱序补录
	mustLimit(t, s, "P1", "pH", 8.0, at(10, 0))
	mustLimit(t, s, "P1", "pH", 6.5, at(1, 0))
	mustLimit(t, s, "P1", "pH", 7.0, at(5, 0))

	// 生效时间相同（含不同时区表示的同一时刻）拒绝，且不覆盖原值
	same := time.Date(2026, 9, 5, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)) // == at(5,0) UTC
	if _, err := s.SetLimit("P1", "pH", 9.9, same); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("same instant in another zone, got %v", err)
	}
	if _, err := s.SetLimit("P1", "pH", 9.9, at(5, 0)); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("same effective time, got %v", err)
	}

	// 原值未被覆盖：确认 at(6,0) 的样品应使用 7.0
	mustSample(t, s, "S1", "P1", at(6, 0), Measurement{Item: "pH", Value: 7.5})
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if smp.Results[0].Limit != 7.0 {
		t.Fatalf("limit overwritten by rejected entry: %+v", smp.Results[0])
	}
}

func TestSubmitSample(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	if _, err := s.SubmitSample("S1", "P9", at(1, 0), Measurement{Item: "pH", Value: 7}); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("unknown point, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", time.Time{}, Measurement{Item: "pH", Value: 7}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("zero time, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(1, 0)); !errors.Is(err, ErrNoMeasurements) {
		t.Fatalf("no measurements, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(1, 0), Measurement{Item: "pH", Value: math.Inf(-1)}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("-Inf, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(1, 0),
		Measurement{Item: " pH ", Value: 7}, Measurement{Item: "pH", Value: 8}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("duplicate item, got %v", err)
	}

	smp := mustSample(t, s, "S1", "P1", at(1, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	if smp.Status != StatusPending {
		t.Fatalf("new sample should be pending, got %s", smp.Status)
	}

	// 同编号同内容（项目顺序不同、时间用另一时区表示）返回原样品
	again, err := s.SubmitSample("S1", "P1",
		time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		Measurement{Item: "COD", Value: 20}, Measurement{Item: "pH", Value: 7})
	if err != nil {
		t.Fatalf("idempotent resubmit: %v", err)
	}
	if again.ID != "S1" || again.Status != StatusPending {
		t.Fatalf("expected original sample, got %+v", again)
	}

	// 任一内容不同则拒绝
	cases := []struct {
		point string
		at    time.Time
		ms    []Measurement
	}{
		{"P1", at(2, 0), []Measurement{{Item: "pH", Value: 7}, {Item: "COD", Value: 20}}},
		{"P1", at(1, 0), []Measurement{{Item: "pH", Value: 7}}},
		{"P1", at(1, 0), []Measurement{{Item: "pH", Value: 7}, {Item: "COD", Value: 21}}},
		{"P1", at(1, 0), []Measurement{{Item: "pH", Value: 7}, {Item: "COD", Value: 20}, {Item: "SS", Value: 5}}},
	}
	for i, c := range cases {
		if _, err := s.SubmitSample("S1", c.point, c.at, c.ms...); !errors.Is(err, ErrSampleConflict) {
			t.Fatalf("case %d: conflicting resubmit should be rejected, got %v", i, err)
		}
	}
}

func TestConfirm(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "pH", 7.0, at(10, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// 缺一个项目的适用上限：拒绝，保持待判定，不留部分结论
	mustSample(t, s, "S1", "P1", at(12, 0),
		Measurement{Item: "pH", Value: 7.5}, Measurement{Item: "SS", Value: 10})
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("missing limit should reject, got %v", err)
	}
	_, ok, err := s.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("no confirmed sample expected")
	}
	list, _ := s.ListByPoint("P1")
	if len(list) != 1 || list[0].Status != StatusPending || list[0].Results != nil {
		t.Fatalf("rejected confirm must leave sample pending without partial results: %+v", list)
	}

	// 补齐限值后可再次确认
	mustLimit(t, s, "P1", "SS", 15.0, at(1, 0))
	smp1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm after backfill: %v", err)
	}
	if smp1.Status != StatusConfirmed || !smp1.Exceeded {
		t.Fatalf("expected confirmed & exceeded, got %+v", smp1)
	}
	// pH 取不晚于采样时间的最近一版（7.0@09-10），7.5 超标；SS 10 ≤ 15 达标
	if smp1.Results[0].Limit != 7.0 || !smp1.Results[0].Exceeded {
		t.Fatalf("pH result wrong: %+v", smp1.Results[0])
	}
	if smp1.Results[1].Limit != 15.0 || smp1.Results[1].Exceeded {
		t.Fatalf("SS result wrong: %+v", smp1.Results[1])
	}

	// 采样时间恰好等于生效时间时使用新值
	mustSample(t, s, "S2", "P1", at(10, 0), Measurement{Item: "pH", Value: 7.5})
	smp2, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	if smp2.Results[0].Limit != 7.0 || !smp2.Results[0].LimitEffective.Equal(at(10, 0)) {
		t.Fatalf("boundary effective time should use new value, got %+v", smp2.Results[0])
	}
	if !smp2.Exceeded {
		t.Fatalf("7.5 > 7.0 should exceed")
	}

	// 等于上限算达标
	mustSample(t, s, "S3", "P1", at(11, 0), Measurement{Item: "pH", Value: 7.0})
	smp3, _ := s.Confirm("S3")
	if smp3.Exceeded || smp3.Results[0].Exceeded {
		t.Fatalf("equal to limit should pass, got %+v", smp3.Results[0])
	}

	// 重复确认直接返回已保存结果；之后补录限值不改变已确认结果
	mustLimit(t, s, "P1", "pH", 100.0, at(10, 12))
	again, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("re-confirm: %v", err)
	}
	if again.Results[0].Limit != 7.0 || !again.Exceeded {
		t.Fatalf("confirmed result must not change, got %+v", again.Results[0])
	}

	if _, err := s.Confirm("nope"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("unknown sample, got %v", err)
	}
}

func TestVoid(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "S2", "P1", at(3, 0), Measurement{Item: "pH", Value: 9})

	if _, err := s.Void("S1", "   "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("blank reason, got %v", err)
	}
	if _, err := s.Void("nope", "x"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("unknown sample, got %v", err)
	}

	// 已确认样品作废后保留结果
	if _, err := s.Confirm("S2"); err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	v, err := s.Void("S2", "  采样瓶破损  ")
	if err != nil {
		t.Fatalf("Void confirmed: %v", err)
	}
	if v.Status != StatusVoided || v.VoidReason != "采样瓶破损" || len(v.Results) != 1 || !v.Exceeded {
		t.Fatalf("voided sample should keep results, got %+v", v)
	}

	// 待判定样品作废后不能再确认
	if _, err := s.Void("S1", "记录错误"); err != nil {
		t.Fatalf("Void pending: %v", err)
	}
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("confirm voided, got %v", err)
	}

	// 重复作废：相同原因（去空白后）返回原记录，不同原因拒绝
	if _, err := s.Void("S1", " 记录错误 "); err != nil {
		t.Fatalf("same reason should return record, got %v", err)
	}
	if _, err := s.Void("S1", "别的原因"); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("different reason should be rejected, got %v", err)
	}

	// 已作废样品的相同内容重报仍返回原样品，不同内容仍拒绝
	if _, err := s.SubmitSample("S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 7}); err != nil {
		t.Fatalf("resubmit voided identical: %v", err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 8}); !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("resubmit voided different, got %v", err)
	}
}

func TestListAndLatest(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	mustSample(t, s, "S2", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "S1", "P1", at(5, 0), Measurement{Item: "pH", Value: 7}) // 同时刻，编号升序
	mustSample(t, s, "S3", "P1", at(8, 0), Measurement{Item: "pH", Value: 9})
	mustSample(t, s, "S4", "P1", at(9, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "SX", "P2", at(10, 0), Measurement{Item: "pH", Value: 7})

	if _, err := s.Confirm("S3"); err != nil {
		t.Fatalf("Confirm S3: %v", err)
	}
	if _, err := s.Void("S4", "作废"); err != nil {
		t.Fatalf("Void S4: %v", err)
	}

	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	ids := []string{list[0].ID, list[1].ID, list[2].ID, list[3].ID}
	want := []string{"S4", "S3", "S1", "S2"} // 时间晚到早，同时刻编号升序
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
	if list[1].Status != StatusConfirmed || !list[1].Exceeded {
		t.Fatalf("S3 should be confirmed & exceeded: %+v", list[1])
	}
	if list[1].Results[0].Value != 9 || list[1].Results[0].Limit != 8.0 {
		t.Fatalf("exceeded item should show value and limit: %+v", list[1].Results[0])
	}
	if list[0].Status != StatusVoided || list[2].Status != StatusPending {
		t.Fatalf("statuses wrong: %+v", list)
	}

	// 最近有效结果：跳过待判定与作废，取 S3
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S3" {
		t.Fatalf("latest = %+v ok=%v err=%v", latest, ok, err)
	}

	// 没有已确认且未作废的样品时明确无结果
	if _, ok, err := s.LatestResult("P2"); err != nil || ok {
		t.Fatalf("P2 should have no result, ok=%v err=%v", ok, err)
	}
}

func TestPersistenceAndIsolation(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := s.Void("S1", "复测"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	// 被拒绝的操作不留记录
	if _, err := s.RegisterPoint("P1", "重复"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("dup point, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重新打开同一目录：记录仍在
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("reopened list = %+v err=%v", list, err)
	}
	if list[0].Status != StatusVoided || list[0].VoidReason != "复测" || len(list[0].Results) != 1 {
		t.Fatalf("reopened sample wrong: %+v", list[0])
	}
	if _, err := s2.RegisterPoint("P1", "再试"); !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("duplicate point after reopen, got %v", err)
	}
	if _, err := s2.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("confirm voided after reopen, got %v", err)
	}

	// 不同目录互不混用
	s3, _ := open(t)
	if _, err := s3.RegisterPoint("P1", "另一个P1"); err != nil {
		t.Fatalf("other dir should be independent: %v", err)
	}
	if _, err := s3.Confirm("S1"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("other dir should not see S1, got %v", err)
	}
}
