package water

import (
	"errors"
	"math"
	"os"
	"path/filepath"
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

// 新增限值保存失败时，当前打开的数据存放必须完整保持调用前的状态：
// 被拒绝的限值不能继续参与判定，原有版本及其适用时间不能丢失或错乱；
// 条件恢复后相同补录可重试成功，且失败期间的其它成功操作不能把
// 被拒绝的限值或版本丢失顺带落盘。
func TestSetLimitPersistFailure(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 10.0, at(1, 0))
	mustLimit(t, s, "P1", "pH", 20.0, at(10, 0))
	mustLimit(t, s, "P1", "pH", 30.0, at(20, 0))

	// 让落盘失败：把临时文件路径占成目录，WriteFile 必然失败
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}
	// 乱序补录到已有版本之间，保存失败
	if _, err := s.SetLimit("P1", "pH", 5.0, at(5, 0)); err == nil {
		t.Fatal("SetLimit should fail when persist fails")
	}
	// 失败发生在更晚生效的版本上同样不能丢原有版本
	if _, err := s.SetLimit("P1", "pH", 35.0, at(25, 0)); err == nil {
		t.Fatal("SetLimit should fail when persist fails")
	}
	// 恢复正常保存条件
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 9/6 采样、值 8：必须使用 9/1 的上限 10 判为达标，不能用被拒绝的 5
	mustSample(t, s, "S1", "P1", at(6, 0), Measurement{Item: "pH", Value: 8})
	smp1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	if smp1.Results[0].Limit != 10.0 || smp1.Exceeded {
		t.Fatalf("rejected limit leaked into judgment: %+v", smp1.Results[0])
	}
	// 9/21 采样、值 25：必须使用 9/20 的上限 30 判为达标，不能因版本丢失退回 20
	mustSample(t, s, "S2", "P1", at(21, 0), Measurement{Item: "pH", Value: 25})
	smp2, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	if smp2.Results[0].Limit != 30.0 || smp2.Exceeded {
		t.Fatalf("original version lost after failed save: %+v", smp2.Results[0])
	}

	// 相同补录可以重试成功，不能把上次失败当成已登记
	mustLimit(t, s, "P1", "pH", 5.0, at(5, 0))

	// 成功补录后，尚未确认且采样于 9/6 的样品使用上限 5
	mustSample(t, s, "S3", "P1", at(6, 0), Measurement{Item: "pH", Value: 8})
	smp3, err := s.Confirm("S3")
	if err != nil {
		t.Fatalf("Confirm S3: %v", err)
	}
	if smp3.Results[0].Limit != 5.0 || !smp3.Exceeded {
		t.Fatalf("backfilled limit should apply to pending sample: %+v", smp3.Results[0])
	}
	// 已确认样品保留原判定及所用限值，重复确认不重新计算
	again1, err := s.Confirm("S1")
	if err != nil || again1.Results[0].Limit != 10.0 || again1.Exceeded {
		t.Fatalf("confirmed result must not change, got %+v err=%v", again1.Results[0], err)
	}
	again2, err := s.Confirm("S2")
	if err != nil || again2.Results[0].Limit != 30.0 || again2.Exceeded {
		t.Fatalf("confirmed result must not change, got %+v err=%v", again2.Results[0], err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重新打开：应看到最后成功保存的限值状态——四版俱在，
	// 被拒绝且未重试的 35@9/25 不能被失败期间的成功操作顺带保存进去
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	mustSample(t, s2, "S4", "P1", at(6, 0), Measurement{Item: "pH", Value: 8})
	smp4, err := s2.Confirm("S4")
	if err != nil || smp4.Results[0].Limit != 5.0 || !smp4.Exceeded {
		t.Fatalf("reopened: 9/6 should use backfilled 5, got %+v err=%v", smp4.Results[0], err)
	}
	mustSample(t, s2, "S5", "P1", at(21, 0), Measurement{Item: "pH", Value: 25})
	smp5, err := s2.Confirm("S5")
	if err != nil || smp5.Results[0].Limit != 30.0 || smp5.Exceeded {
		t.Fatalf("reopened: 9/21 should use 30, got %+v err=%v", smp5.Results[0], err)
	}
	mustSample(t, s2, "S6", "P1", at(24, 0), Measurement{Item: "pH", Value: 31})
	smp6, err := s2.Confirm("S6")
	if err != nil || smp6.Results[0].Limit != 30.0 || !smp6.Exceeded {
		t.Fatalf("reopened: rejected 35@9/25 must not exist, got %+v err=%v", smp6.Results[0], err)
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

// 确认样品时，各项目都已按采样当时适用的上限完成判定、但最后本地落盘失败的情况下，
// 必须返回保存错误，不能把已经算出的逐项结果当成确认成功返回；
// 样品保持待判定、无逐项判定结果、无失败尝试留下的超标标记，
// 最近有效结果也不能因为失败样品采样更晚而换成它。
// 恢复保存条件后再次确认应能成功保存，不能被当成已经确认而直接返回失败尝试的状态。
func TestConfirmPersistFailure(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")

	// 已有采样点中一份正常保存的已确认样品（采样更早，值达标）
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 7})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}

	// 采样更晚的待判定样品：两个项目在采样时均有适用上限，
	// 一个值超过上限，另一个值等于上限
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S2", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 9},
		Measurement{Item: "COD", Value: 30})

	// 让本次确认的本地保存失败：临时文件路径被占成目录，WriteFile 必然失败。
	// 采样点、限值和样品都已在此前成功准备好，失败只发生在这一次确认的保存上。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}
	_, err := s.Confirm("S2")
	if err == nil {
		t.Fatal("Confirm should return a save error when persist fails")
	}
	// 必须是保存错误，而不是缺限值、样品不存在等判定/录入错误：
	// 两个项目在采样时都有适用上限，逐项判定已完成才会走到保存
	for _, judgeErr := range []error{ErrMissingLimit, ErrUnknownSample, ErrVoided, ErrEmptyField, ErrClosed} {
		if errors.Is(err, judgeErr) {
			t.Fatalf("failure must be a save error, not a judgment/entry error %v: %v", judgeErr, err)
		}
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 同一份打开的存放中按采样点查看：S2 仍是原样——
	// 待判定、无逐项判定结果、无失败尝试产生的超标标记
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	if len(list) != 2 || list[0].ID != "S2" || list[1].ID != "S1" {
		t.Fatalf("list order/content wrong: %+v", list)
	}
	pending := list[0]
	if pending.Status != StatusPending || pending.Results != nil || pending.Exceeded {
		t.Fatalf("failed confirm must leave sample pending without results/exceeded flag: %+v", pending)
	}
	if !pending.SampledAt.Equal(at(5, 0)) ||
		len(pending.Measurements) != 2 ||
		pending.Measurements[0].Item != "pH" || pending.Measurements[0].Value != 9 ||
		pending.Measurements[1].Item != "COD" || pending.Measurements[1].Value != 30 {
		t.Fatalf("sampled time/items/values should be unchanged: %+v", pending)
	}
	// 原有已确认样品的结论和所用限值保持原样
	old := list[1]
	if old.Status != StatusConfirmed || old.Exceeded ||
		len(old.Results) != 1 || old.Results[0].Item != "pH" ||
		old.Results[0].Value != 7 || old.Results[0].Limit != 8.0 ||
		!old.Results[0].LimitEffective.Equal(at(1, 0)) {
		t.Fatalf("S1 must keep its confirmed conclusion and limit: %+v", old)
	}

	// 最近有效结果仍是之前那份已确认样品：不能因失败的样品采样更晚就把它作为最新结论
	latest1, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest1.ID != "S1" {
		t.Fatalf("latest should still be S1, got %+v ok=%v err=%v", latest1, ok, err)
	}

	// 关闭后重新打开：失败尝试不能把部分判定结果写到盘上
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	disk, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint after reopen: %v", err)
	}
	if len(disk) != 2 || disk[0].ID != "S2" {
		t.Fatalf("reopened list wrong: %+v", disk)
	}
	if disk[0].Status != StatusPending || disk[0].Results != nil || disk[0].Exceeded {
		t.Fatalf("disk must not carry partial results from failed confirm: %+v", disk[0])
	}
	if _, ok, err := s2.LatestResult("P1"); err != nil || !ok {
		t.Fatalf("latest after reopen should still exist, ok=%v err=%v", ok, err)
	}

	// 恢复正常保存条件后再确认：应按采样当时适用的限值成功保存，
	// 不能被当成已经确认而直接返回失败尝试的状态
	smp, err := s2.Confirm("S2")
	if err != nil {
		t.Fatalf("Confirm S2 after restore: %v", err)
	}
	if smp.Status != StatusConfirmed || !smp.Exceeded {
		t.Fatalf("S2 should be confirmed & exceeded: %+v", smp)
	}
	if len(smp.Results) != 2 {
		t.Fatalf("expected item-by-item results for both measurements, got %+v", smp.Results)
	}
	// pH 9 > 8 超标；COD 30 == 30 达标（测量值大于上限才超标，等于不算）
	r0, r1 := smp.Results[0], smp.Results[1]
	if r0.Item != "pH" || r0.Value != 9 || r0.Limit != 8.0 ||
		!r0.LimitEffective.Equal(at(1, 0)) || !r0.Exceeded {
		t.Fatalf("pH judgment wrong: %+v", r0)
	}
	if r1.Item != "COD" || r1.Value != 30 || r1.Limit != 30.0 ||
		!r1.LimitEffective.Equal(at(1, 0)) || r1.Exceeded {
		t.Fatalf("COD equal to limit must pass: %+v", r1)
	}

	// 成功后按采样点查看的记录与最近有效结果应一致，
	// 逐项核对测量值、所用上限、生效时间和各自的判定
	list2, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint after successful confirm: %v", err)
	}
	if len(list2) != 2 || list2[0].ID != "S2" ||
		list2[0].Status != StatusConfirmed || !list2[0].Exceeded {
		t.Fatalf("list should show S2 confirmed & exceeded: %+v", list2)
	}
	latest2, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S2" {
		t.Fatalf("latest should now be S2, got %+v ok=%v err=%v", latest2, ok, err)
	}
	for i, r := range latest2.Results {
		lr := list2[0].Results[i]
		if r.Item != lr.Item || r.Value != lr.Value || r.Limit != lr.Limit ||
			!r.LimitEffective.Equal(lr.LimitEffective) || r.Exceeded != lr.Exceeded {
			t.Fatalf("list and latest disagree on item %d: list=%+v latest=%+v", i, lr, r)
		}
	}
}
