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

// 新样品的提交内容本身合法、但本地数据写入失败时，本次录入必须整体失败：
// 返回保存错误和空样品，不能返回带编号的待判定记录。按采样点查看仍只有
// 此前成功保存的样品；对失败提交的编号请求确认，必须明确报样品不存在，
// 不能转而开始判定，也不能报缺少适用上限。较早样品的原始测量、确认状态、
// 逐项所用上限与生效时间以及整份结论保持原样，最近有效结果仍指向它。
// 保存条件未恢复期间，同编号同内容再次提交仍因保存失败被拒绝，不能走已有
// 样品的幂等重报返回成功；恢复后用同编号、把其中一个测量值改为另一个有限数
// 再次录入，应作为首次成功录入接受，不能因前一次失败内容不同而报冲突。
// 失败尝试的测量值不能被随后一次成功录入顺带保存，重新打开数据存放后
// 只看得到成功提交的内容。同一份提交内项目重复属于内容校验，即使本地同样
// 无法写入也必须返回现有的项目重复错误，不能与合法提交遇写入失败混为一种失败。
func TestSubmitSamplePersistFailure(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	find := func(list []Sample, id string) Sample {
		t.Helper()
		for _, smp := range list {
			if smp.ID == id {
				return smp
			}
		}
		t.Fatalf("sample %q missing from list %+v", id, list)
		return Sample{}
	}
	valuesOf := func(smp Sample) map[string]float64 {
		m := make(map[string]float64, len(smp.Measurements))
		for _, msm := range smp.Measurements {
			m[msm.Item] = msm.Value
		}
		return m
	}
	// 较早样品 S1 应始终保持的完整记录：pH 9 > 8@09-01 超标，
	// COD 30 == 30@09-01 达标，整份超标，已确认。
	checkEarlier := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
			t.Fatalf("%s: 较早样品的编号、采样点、状态或作废原因被改动: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(at(5, 0)) || !smp.Exceeded {
			t.Fatalf("%s: 采样时间或整份结论被改动: %+v", label, smp)
		}
		if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("%s: 测量或逐项结果数量被改动: %+v", label, smp)
		}
		if ms := valuesOf(smp); ms["pH"] != 9 || ms["COD"] != 30 {
			t.Fatalf("%s: 原始测量被改动: %+v", label, smp.Measurements)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if ph := rs["pH"]; ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
			t.Fatalf("%s: pH 逐项所用上限与生效时间被改动: %+v", label, ph)
		}
		if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
			t.Fatalf("%s: COD 逐项所用上限与生效时间被改动: %+v", label, cod)
		}
	}

	// 较早、采样于 9/5 的双项目样品正常录入并确认，作为此前唯一成功保存的样品。
	mustSample(t, s, "S1", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	conf1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1 setup: %v", err)
	}
	checkEarlier(conf1, "setup 确认返回值")

	// 只让新样品的保存失败：采样点、限值和较早样品此前均已成功落盘。
	// 占用临时文件路径为目录后，原子写的 WriteFile 必然失败，不依赖目录权限。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 合法的新样品：编号全新、采样时间更晚、两个不同项目，测量值都是有限数。
	failed := []Measurement{{Item: "pH", Value: 7}, {Item: "COD", Value: 25}}
	got, err := s.SubmitSample("S2", "P1", at(10, 0), failed...)
	if err == nil {
		t.Fatal("SubmitSample should fail when the new sample cannot be saved")
	}
	// 必须是保存错误，不能冒充任何一种提交内容校验失败。
	for _, target := range []error{
		ErrEmptyField, ErrInvalidTime, ErrNoMeasurements, ErrInvalidValue,
		ErrDuplicateItem, ErrUnknownPoint, ErrSampleConflict,
	} {
		if errors.Is(err, target) {
			t.Fatalf("save failure must not be reported as %v: %v", target, err)
		}
	}
	// 返回空样品：连编号和待判定状态都没有，调用方不能当录入成功使用。
	if got.ID != "" || got.Status != "" {
		t.Fatalf("failed save must return an empty sample, got %+v", got)
	}
	// 内存状态同步回滚，随后的成功保存不能把失败尝试顺带写入。
	if _, ok := s.samples["S2"]; ok {
		t.Fatal("failed sample must be rolled back from the in-memory store")
	}

	// 按采样点查看仍只有此前成功保存的 S1，原测量、判定依据与整份结论原样。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("ListByPoint after failed save: %+v err=%v", list, err)
	}
	checkEarlier(list[0], "保存失败后按点查看")
	// 重复确认较早样品，仍返回原来保存的结论。
	again1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1 after failed save: %v", err)
	}
	checkEarlier(again1, "保存失败后重复确认较早样品")

	// 对失败提交的编号请求确认：明确报样品不存在。两个项目的适用上限都在，
	// 因此这既不能转而开始判定，也不能是缺少适用上限。
	probe, err := s.Confirm("S2")
	if !errors.Is(err, ErrUnknownSample) || probe.ID != "" {
		t.Fatalf("failed submit must leave no sample to confirm, got sample=%+v err=%v", probe, err)
	}

	// 最近有效结果仍指向较早样品，逐项依据与整份结论保持原样。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("latest valid result should stay S1, got %+v ok=%v err=%v", latest, ok, err)
	}
	checkEarlier(latest, "保存失败后最近有效结果")

	// 保存条件尚未恢复：同编号、同内容再次提交，仍应因保存失败被拒绝，
	// 不能因为编号“似乎提交过”而走已有样品的幂等重报返回成功。
	got2, err := s.SubmitSample("S2", "P1", at(10, 0), failed...)
	if err == nil {
		t.Fatal("resubmit while saving still fails should be rejected")
	}
	if errors.Is(err, ErrSampleConflict) || errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("repeated valid submit must fail with the save error, got %v", err)
	}
	if got2.ID != "" || got2.Status != "" {
		t.Fatalf("repeated failed save must return an empty sample, got %+v", got2)
	}

	// 同一份提交内项目重复属于内容校验：即使本地同样无法写入，也必须在保存
	// 之前返回现有的项目重复错误，不能与合法提交遇写入失败混为一种失败。
	if _, err := s.SubmitSample("S2", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "pH", Value: 8}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("duplicate item must be rejected before save (same id), got %v", err)
	}
	if _, err := s.SubmitSample("S9", "P1", at(10, 0),
		Measurement{Item: "COD", Value: 1}, Measurement{Item: "COD", Value: 2}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("duplicate item must be rejected before save (new id), got %v", err)
	}
	// 被内容校验拒绝的提交同样不能留下编号，成功录入后也不能把它顺带落盘。
	if _, ok := s.samples["S9"]; ok {
		t.Fatal("duplicate-item submission must not leave a sample")
	}

	// 恢复正常保存条件。
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 用同一编号继续录入，把 pH 改成另一个有限数 6（项目顺序也调换，顺序无关）：
	// 必须作为首次成功录入接受并置为待判定，不能因与失败尝试内容不同而报冲突。
	saved, err := s.SubmitSample("S2", "P1", at(10, 0),
		Measurement{Item: "COD", Value: 25}, Measurement{Item: "pH", Value: 6})
	if err != nil {
		t.Fatalf("submit after save restored should succeed: %v", err)
	}
	if saved.ID != "S2" || saved.PointID != "P1" || saved.Status != StatusPending {
		t.Fatalf("saved sample should be pending: %+v", saved)
	}
	if saved.Exceeded || saved.Results != nil || saved.VoidReason != "" {
		t.Fatalf("new sample must carry no verdict or void reason: %+v", saved)
	}
	if vs := valuesOf(saved); len(vs) != 2 || vs["pH"] != 6 || vs["COD"] != 25 {
		t.Fatalf("saved measurements should match this submission: %+v", saved.Measurements)
	}

	// 已成功保存样品的重复提交行为保持不变：同内容（顺序无关）幂等返回原记录；
	// 不同内容仍整次拒绝——哪怕填入的正是上次失败尝试里的值 7。
	idem, err := s.SubmitSample("S2", "P1", at(10, 0),
		Measurement{Item: "COD", Value: 25}, Measurement{Item: "pH", Value: 6})
	if err != nil || idem.ID != "S2" || idem.Status != StatusPending || valuesOf(idem)["pH"] != 6 {
		t.Fatalf("identical resubmit should return the saved pending sample, got %+v err=%v", idem, err)
	}
	if _, err := s.SubmitSample("S2", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 25}); !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("different content against the saved sample should conflict, got %v", err)
	}

	// 查询中只出现一份该编号的待判定样品，两个项目及数值以本次成功提交为准，
	// 没有判定结果或作废原因；较早样品仍是原样的已确认记录。
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("ListByPoint after recovery: %+v err=%v", list2, err)
	}
	if list2[0].ID != "S2" || list2[1].ID != "S1" {
		t.Fatalf("order should put the later S2 first: %+v", list2)
	}
	fresh := find(list2, "S2")
	if fresh.Status != StatusPending || fresh.Exceeded || fresh.Results != nil || fresh.VoidReason != "" {
		t.Fatalf("S2 should be a single pending sample without verdict: %+v", fresh)
	}
	if vs := valuesOf(fresh); len(vs) != 2 || vs["pH"] != 6 || vs["COD"] != 25 {
		t.Fatalf("S2 measurements must come from the successful submission: %+v", fresh.Measurements)
	}
	checkEarlier(find(list2, "S1"), "恢复后较早样品")
	// 新样品尚未确认，最近有效结果仍指向较早的 S1。
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S1" {
		t.Fatalf("latest valid result should still be S1, got %+v ok=%v err=%v", latest2, ok, err)
	}
	checkEarlier(latest2, "恢复后最近有效结果")

	// 重新打开同一数据存放：成功提交的内容仍在，失败尝试的测量值 7 没有混入；
	// 较早样品的确认记录与最近有效结果身份也保持不变。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list3, err := s2.ListByPoint("P1")
	if err != nil || len(list3) != 2 {
		t.Fatalf("reopened ListByPoint: %+v err=%v", list3, err)
	}
	reopened := find(list3, "S2")
	if reopened.Status != StatusPending || reopened.Exceeded || reopened.Results != nil || reopened.VoidReason != "" {
		t.Fatalf("reopened S2 should stay pending without verdict: %+v", reopened)
	}
	if vs := valuesOf(reopened); len(vs) != 2 || vs["pH"] != 6 || vs["COD"] != 25 {
		t.Fatalf("failed-attempt measurement leaked into the saved record: %+v", reopened.Measurements)
	}
	checkEarlier(find(list3, "S1"), "重开后较早样品")
	latest3, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest3.ID != "S1" {
		t.Fatalf("reopened latest should stay S1, got %+v ok=%v err=%v", latest3, ok, err)
	}
	checkEarlier(latest3, "重开后最近有效结果")
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

// 所有项目都能完成判定、但本地保存结果失败时，本次确认必须整体失败：
// 已算出的逐项结果与整份样品的超标标记都不能生效，样品保持待判定，
// 采样点上一份已确认样品仍是最近有效结果；保存条件恢复后可重新确认，
// 按采样当时适用的限值完整保存逐项判定（一项超标、一项等于上限）。
func TestConfirmPersistFailure(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	resultByItem := func(rs []ItemResult) map[string]ItemResult {
		m := make(map[string]ItemResult, len(rs))
		for _, r := range rs {
			m[r.Item] = r
		}
		return m
	}
	find := func(list []Sample, id string) Sample {
		t.Helper()
		for _, smp := range list {
			if smp.ID == id {
				return smp
			}
		}
		t.Fatalf("sample %q missing from list %+v", id, list)
		return Sample{}
	}

	// 采样时间较早、正常保存的已确认样品：pH 7 ≤ 8，达标
	mustSample(t, s, "S1", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	conf1, err := s.Confirm("S1")
	if err != nil || conf1.Status != StatusConfirmed || conf1.Exceeded {
		t.Fatalf("Confirm S1 setup: %+v err=%v", conf1, err)
	}

	// 更晚的待判定样品：两个项目采样时均有适用上限，pH 超上限，COD 恰好等于上限
	mustSample(t, s, "S2", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	// 对照样品：缺少适用上限，用来证明本检查区分判定错误与保存错误
	mustSample(t, s, "S3", "P1", at(2, 0), Measurement{Item: "SS", Value: 1})

	// 只让确认结果的保存失败：采样点、限值和样品准备此前均已成功落盘。
	// 占用临时文件路径为目录后，原子写的 WriteFile 必然失败，不依赖目录权限。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 同样无法保存的条件下，缺上限在判定阶段即以 ErrMissingLimit 拒绝，
	// 根本走不到保存；S2 的失败必须是与之不同的保存错误。
	if _, err := s.Confirm("S3"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("missing limit should be a judgment error, got %v", err)
	}
	got, err := s.Confirm("S2")
	if err == nil {
		t.Fatal("Confirm should fail when results cannot be saved")
	}
	if errors.Is(err, ErrMissingLimit) {
		t.Fatalf("save failure must not be reported as missing limit: %v", err)
	}
	if got.ID != "" || got.Status == StatusConfirmed {
		t.Fatalf("failed save must not return a confirmed sample: %+v", got)
	}

	// 按采样点查看：原采样时间、项目和测量值保持，状态待判定，
	// 没有逐项判定结果，整份样品也不能带上失败尝试算出的超标标记。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint after failed save: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list should keep all three samples, got %+v", list)
	}
	failed := find(list, "S2")
	if failed.Status != StatusPending || failed.Exceeded || failed.Results != nil {
		t.Fatalf("failed confirm must leave pending sample without results: %+v", failed)
	}
	if !failed.SampledAt.Equal(at(10, 0)) || len(failed.Measurements) != 2 {
		t.Fatalf("sampled time and measurements changed: %+v", failed)
	}
	values := map[string]float64{}
	for _, m := range failed.Measurements {
		values[m.Item] = m.Value
	}
	if values["pH"] != 9 || values["COD"] != 30 {
		t.Fatalf("measurements changed: %+v", failed.Measurements)
	}
	if s3 := find(list, "S3"); s3.Status != StatusPending || s3.Results != nil {
		t.Fatalf("S3 should stay pending without results: %+v", s3)
	}

	// 同一份已打开的数据存放中，最近有效结果仍是较早的 S1，
	// 结论与所用限值保持原样，不能被采样更晚的失败尝试顶替。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("latest valid result should stay S1, got %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.Exceeded || len(latest.Results) != 1 {
		t.Fatalf("S1 conclusion changed: %+v", latest)
	}
	r1 := latest.Results[0]
	if r1.Item != "pH" || r1.Value != 7 || r1.Limit != 8.0 || !r1.LimitEffective.Equal(at(1, 0)) || r1.Exceeded {
		t.Fatalf("S1 result and limit must remain intact: %+v", r1)
	}

	// 恢复正常保存条件
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}
	// 采样时间之后才生效的新版上限不能影响按采样当时适用限值的重新判定
	mustLimit(t, s, "P1", "pH", 9.5, at(15, 0))

	// 重新确认：不能被当成已确认而直接返回失败尝试的状态，必须重新判定并成功保存
	conf2, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("retry Confirm after save restored: %v", err)
	}
	checkSaved := func(smp Sample, label string) {
		t.Helper()
		if smp.Status != StatusConfirmed || !smp.SampledAt.Equal(at(10, 0)) || !smp.Exceeded || len(smp.Results) != 2 {
			t.Fatalf("%s: confirmed sample wrong: %+v", label, smp)
		}
		rs := resultByItem(smp.Results)
		ph, cod := rs["pH"], rs["COD"]
		if ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
			t.Fatalf("%s: pH should be 9 > 8@09-01 exceeded, got %+v", label, ph)
		}
		if cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
			t.Fatalf("%s: COD 30 == limit 30@09-01 should pass, got %+v", label, cod)
		}
	}
	checkSaved(conf2, "retry result")

	// 重复确认返回已保存结果
	again, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("re-confirm S2: %v", err)
	}
	checkSaved(again, "re-confirm")
	// 原样品的结论与所用限值保持原样，不受新限值影响
	keep1, err := s.Confirm("S1")
	if err != nil || keep1.Exceeded || keep1.Results[0].Limit != 8.0 || !keep1.Results[0].LimitEffective.Equal(at(1, 0)) {
		t.Fatalf("S1 must remain confirmed as saved, got %+v err=%v", keep1, err)
	}

	// 按采样点查看的记录与最近有效结果一致
	list2, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint after recovery: %v", err)
	}
	saved := find(list2, "S2")
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S2" {
		t.Fatalf("later S2 should now be the latest valid result, got %+v ok=%v err=%v", latest2, ok, err)
	}
	checkSaved(saved, "list")
	checkSaved(latest2, "latest")
	if s3 := find(list2, "S3"); s3.Status != StatusPending || s3.Results != nil || s3.Exceeded {
		t.Fatalf("S3 should still be pending with no failure marker: %+v", s3)
	}

	// 重新打开同一数据存放：成功保存的结果落盘，失败尝试不留任何痕迹
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list3, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("reopened ListByPoint: %v", err)
	}
	latest3, ok, err := s2.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("reopened latest result: %+v ok=%v err=%v", latest3, ok, err)
	}
	checkSaved(find(list3, "S2"), "reopened list")
	checkSaved(latest3, "reopened latest")
	if r := find(list3, "S1"); r.Status != StatusConfirmed || r.Exceeded || r.Results[0].Limit != 8.0 {
		t.Fatalf("reopened S1 changed: %+v", r)
	}
	if r := find(list3, "S3"); r.Status != StatusPending || r.Results != nil {
		t.Fatalf("reopened S3 should stay pending: %+v", r)
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

// 已确认样品的作废请求合法、样品也存在，但本地保存作废记录失败时，
// 本次作废必须整体失败：样品继续按原来的已确认记录使用，状态不能先变成作废，
// 作废原因不能留下，逐项判定依据与整份超标标记保持原样，最近有效结果不消失。
// 保存条件恢复后，用另一个合法原因重新作废应正常成功，不能把上次失败时
// 填写的原因当作已有原因报冲突；成功后最近有效结果退到较早的已确认样品，
// 已作废样品再次确认明确拒绝。
func TestVoidPersistFailure(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	find := func(list []Sample, id string) Sample {
		t.Helper()
		for _, smp := range list {
			if smp.ID == id {
				return smp
			}
		}
		t.Fatalf("sample %q missing from list %+v", id, list)
		return Sample{}
	}
	// 较晚样品 S2 的原已确认结论：pH 9 > 8 超标，COD 30 == 30 达标。
	// wantStatus/wantReason 给出应有的状态与作废原因，其余字段都应保持原记录。
	checkLater := func(smp Sample, wantStatus Status, wantReason, label string) {
		t.Helper()
		if smp.ID != "S2" || smp.PointID != "P1" || smp.Status != wantStatus || smp.VoidReason != wantReason {
			t.Fatalf("%s: 基本字段、状态或作废原因被改动: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(at(10, 0)) {
			t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
		}
		if !smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("%s: 整份超标标记或记录数量被改动: %+v", label, smp)
		}
		ms := map[string]float64{}
		for _, m := range smp.Measurements {
			ms[m.Item] = m.Value
		}
		if ms["pH"] != 9 || ms["COD"] != 30 {
			t.Fatalf("%s: 原始测量值被改动: %+v", label, smp.Measurements)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if ph := rs["pH"]; ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
			t.Fatalf("%s: pH 超标项被改动: %+v", label, ph)
		}
		if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
			t.Fatalf("%s: COD 恰好等于上限应仍达标: %+v", label, cod)
		}
	}

	// 同一采样点两份已确认样品：S1 较早且达标，S2 较晚、双项目、整份超标
	mustSample(t, s, "S1", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	if c1, err := s.Confirm("S1"); err != nil || c1.Exceeded || c1.Status != StatusConfirmed {
		t.Fatalf("Confirm S1 setup: %+v err=%v", c1, err)
	}
	mustSample(t, s, "S2", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S2"); err != nil {
		t.Fatalf("Confirm S2 setup: %v", err)
	}

	// 只让作废记录的保存失败：样品、限值、确认结果此前均已成功落盘。
	// 占用临时文件路径为目录后，原子写的 WriteFile 必然失败，不依赖目录权限。
	tmp := filepath.Join(dir, "water-data.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("block persist: %v", err)
	}

	// 保存失败的条件下，空原因与不存在样品仍在保存之前的校验阶段拒绝，
	// 用来证明下面 S2 的失败不是这类拒绝冒充的。
	if _, err := s.Void("S2", "   "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("blank reason should be rejected before save, got %v", err)
	}
	if _, err := s.Void("nope", "原因"); !errors.Is(err, ErrUnknownSample) {
		t.Fatalf("unknown sample should be rejected before save, got %v", err)
	}

	// 合法的作废请求（样品存在、已确认、原因非空）走到保存阶段后失败：
	// 必须返回保存错误，不能返回作废成功的记录。
	failedReason := "  采样瓶破损  "
	got, err := s.Void("S2", failedReason)
	if err == nil {
		t.Fatal("Void should fail when the void record cannot be saved")
	}
	for _, target := range []error{ErrEmptyField, ErrUnknownSample, ErrVoided, ErrVoidReasonConflict} {
		if errors.Is(err, target) {
			t.Fatalf("save failure must not be reported as %v: %v", target, err)
		}
	}
	if got.ID != "" || got.Status == StatusVoided {
		t.Fatalf("failed save must not return a voided sample: %+v", got)
	}

	// 失败后按采样点查看：较晚样品仍为已确认、作废原因为空，
	// 编号、采样点、采样时间、两个项目的原始测量与原判定依据全部保持原样。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint after failed save: %+v err=%v", list, err)
	}
	checkLater(find(list, "S2"), StatusConfirmed, "", "失败后按点查看")
	if s1 := find(list, "S1"); s1.Status != StatusConfirmed || s1.Exceeded || s1.VoidReason != "" {
		t.Fatalf("较早样品不应受失败作废影响: %+v", s1)
	}

	// 最近有效结果仍指向较晚的 S2，不能退到 S1，也不能变成没有结果。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S2" {
		t.Fatalf("latest valid result should stay S2, got %+v ok=%v err=%v", latest, ok, err)
	}
	checkLater(latest, StatusConfirmed, "", "失败后最近有效结果")

	// 再次确认应返回原来保存的结论，不能因失败的作废请求拒绝确认。
	again, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("re-confirm after failed void: %v", err)
	}
	checkLater(again, StatusConfirmed, "", "失败后重复确认")

	// 恢复正常保存条件
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("restore persist: %v", err)
	}

	// 用另一个合法原因重新作废同一份样品：必须成功，
	// 不能把上次失败时填写的原因当成已有原因而报冲突。
	v, err := s.Void("S2", "复测确认超标")
	if err != nil {
		t.Fatalf("retry Void after save restored: %v", err)
	}
	if v.Status != StatusVoided || v.VoidReason != "复测确认超标" || v.VoidReason == failedReason {
		t.Fatalf("void should be saved with the new reason, got %+v", v)
	}
	// 成功记录保留两个项目的原始测量和原判定依据，并显示本次提交的原因
	checkLater(v, StatusVoided, "复测确认超标", "成功作废返回值")

	// 按点查看仍能找到作废记录，原测量与原判定依据保留；最近有效结果改为较早的 S1
	list2, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint after recovery: %v", err)
	}
	checkLater(find(list2, "S2"), StatusVoided, "复测确认超标", "成功作废后按点查看")
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S1" {
		t.Fatalf("latest valid result should fall back to S1, got %+v ok=%v err=%v", latest2, ok, err)
	}
	// 已作废样品再次确认明确拒绝
	if _, err := s.Confirm("S2"); !errors.Is(err, ErrVoided) {
		t.Fatalf("confirm voided sample should be rejected, got %v", err)
	}
	// 公开语义保持兼容：相同保存原因幂等返回，其它原因仍报冲突
	if same, err := s.Void("S2", " 复测确认超标 "); err != nil || same.VoidReason != "复测确认超标" {
		t.Fatalf("same reason should stay idempotent, got %+v err=%v", same, err)
	}
	if _, err := s.Void("S2", failedReason); !errors.Is(err, ErrVoidReasonConflict) {
		t.Fatalf("a different reason should conflict with the saved one, got %v", err)
	}

	// 重新打开同一数据存放：成功作废落盘，失败尝试不留任何痕迹
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list3, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("reopened ListByPoint: %v", err)
	}
	checkLater(find(list3, "S2"), StatusVoided, "复测确认超标", "重开后按点查看")
	if latest3, ok, err := s2.LatestResult("P1"); err != nil || !ok || latest3.ID != "S1" {
		t.Fatalf("reopened latest should be S1, got %+v ok=%v err=%v", latest3, ok, err)
	}
	if _, err := s2.Confirm("S2"); !errors.Is(err, ErrVoided) {
		t.Fatalf("reopened confirm voided should be rejected, got %v", err)
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
