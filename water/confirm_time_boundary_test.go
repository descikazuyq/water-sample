package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：确认时按“采样时刻”选用限值，采样与生效时间都
// 带小数秒，同一秒内相差一纳秒的不同采样时刻必须选用不同版本。现有录入、
// 确认、查询的入口与业务行为保持现状。

// 浊度上限在同一秒的第 500000000 纳秒由 10 改为 5，三份浊度都是 7 的样品分别
// 采样于生效前一纳秒、恰好生效、后一纳秒：第一份用上限 10 达标，后两份用上限 5
// 超标。即使先登记新版本再乱序补录旧版本，确认结果也只由各版生效时刻与采样时刻
// 的关系决定。每份样品还带一个上限未变更、测量值恰好等于上限的项目，它在三份
// 样品中都达标且所用限值一致，不能冲掉浊度的超标结论。逐项判定要保留实际采用的
// 上限数值及准确生效时刻，采样时间的小数秒不能丢失；确认返回值与按采样点查询到
// 的记录一致，重新打开数据存放后小数秒与判定依据仍在。
func TestConfirmNanosecondBoundary(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")

	oldEff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// 新上限在某一秒的第 500000000 纳秒起生效
	newEff := time.Date(2026, 9, 10, 12, 0, 0, 500000000, time.UTC)

	// 先登记新版本，再乱序补录旧版本：判定只由生效时刻决定，与登记顺序无关
	mustLimit(t, s, "P1", "浊度", 5.0, newEff)
	mustLimit(t, s, "P1", "浊度", 10.0, oldEff)
	// pH 上限始终只有一版，测量值恰好等于上限，三份样品都应达标
	mustLimit(t, s, "P1", "pH", 8.0, oldEff)

	before := newEff.Add(-time.Nanosecond)
	after := newEff.Add(time.Nanosecond)
	samples := []struct {
		id        string
		sampledAt time.Time
		wantLimit float64   // 浊度实际应采用的上限
		wantEff   time.Time // 该上限的准确生效时刻
		exceeded  bool      // 整份样品结论
	}{
		{"S-before", before, 10.0, oldEff, false},
		{"S-exact", newEff, 5.0, newEff, true},
		{"S-after", after, 5.0, newEff, true},
	}
	for _, c := range samples {
		mustSample(t, s, c.id, "P1", c.sampledAt,
			Measurement{Item: "浊度", Value: 7}, Measurement{Item: "pH", Value: 8})
	}

	// check 核对一份已确认样品：采样时刻（含小数秒）、两份测量值、逐项采用的
	// 上限与准确生效时刻、逐项与整份结论。
	check := func(smp Sample, c struct {
		id        string
		sampledAt time.Time
		wantLimit float64
		wantEff   time.Time
		exceeded  bool
	}, label string) {
		t.Helper()
		if smp.ID != c.id || smp.PointID != "P1" || smp.Status != StatusConfirmed {
			t.Fatalf("%s: 样品基本字段错误: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(c.sampledAt) || smp.SampledAt.Nanosecond() != c.sampledAt.Nanosecond() {
			t.Fatalf("%s: 采样时刻丢失小数秒或被改动: %v", label, smp.SampledAt)
		}
		if smp.Exceeded != c.exceeded {
			t.Fatalf("%s: 整份结论应为超标=%v: %+v", label, c.exceeded, smp)
		}
		ms := map[string]float64{}
		for _, m := range smp.Measurements {
			ms[m.Item] = m.Value
		}
		if len(ms) != 2 || ms["浊度"] != 7 || ms["pH"] != 8 {
			t.Fatalf("%s: 原测量值被改动: %+v", label, smp.Measurements)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if len(rs) != 2 {
			t.Fatalf("%s: 逐项结果数量错误: %+v", label, smp.Results)
		}
		turb := rs["浊度"]
		if turb.Value != 7 || turb.Limit != c.wantLimit ||
			!turb.LimitEffective.Equal(c.wantEff) ||
			turb.LimitEffective.Nanosecond() != c.wantEff.Nanosecond() ||
			turb.Exceeded != (7 > c.wantLimit) {
			t.Fatalf("%s: 浊度应为 7 对比上限 %g（%v 生效）: %+v",
				label, c.wantLimit, c.wantEff, turb)
		}
		// pH 上限未变更、测量值恰好等于上限：三份样品都达标，依据一致
		ph := rs["pH"]
		if ph.Value != 8 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(oldEff) || ph.Exceeded {
			t.Fatalf("%s: pH 8 == 上限 8@09-01 应达标且依据一致: %+v", label, ph)
		}
	}

	confirmed := map[string]Sample{}
	for _, c := range samples {
		smp, err := s.Confirm(c.id)
		if err != nil {
			t.Fatalf("Confirm(%q): %v", c.id, err)
		}
		check(smp, c, "确认返回值")
		confirmed[c.id] = smp
	}

	// 按采样点查询：从晚到早为 S-after、S-exact、S-before，
	// 每份记录与确认返回值一致，各自保留采样时刻与测量值。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 3 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	wantOrder := []string{"S-after", "S-exact", "S-before"}
	for i, id := range wantOrder {
		if list[i].ID != id {
			t.Fatalf("按点查询顺序 = %v, 期望 %v",
				[]string{list[0].ID, list[1].ID, list[2].ID}, wantOrder)
		}
	}
	for _, c := range samples {
		var listed Sample
		for _, smp := range list {
			if smp.ID == c.id {
				listed = smp
			}
		}
		check(listed, c, "按点查询")
		if !reflect.DeepEqual(listed, confirmed[c.id]) {
			t.Fatalf("按点查询记录与确认返回值不一致:\n查询 %+v\n确认 %+v", listed, confirmed[c.id])
		}
	}

	// 关闭后重新打开：小数秒、逐项所用上限与准确生效时刻、整份结论原样保留
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list2, err := s2.ListByPoint("P1")
	if err != nil || len(list2) != 3 {
		t.Fatalf("reopened ListByPoint: %+v err=%v", list2, err)
	}
	for _, c := range samples {
		for _, smp := range list2 {
			if smp.ID == c.id {
				check(smp, c, "重开后按点查询")
			}
		}
	}
}

// 采样时某项目没有任何已生效版本：唯一一版上限比采样时间晚一纳秒才生效。
// 即使另一项能判定，也必须因缺少适用限值整次拒绝——不能借用这个未来版本，
// 也不能把缺失上限当成零。原样品继续待判定，没有逐项结果或整份结论。
// 补录一版不晚于采样时刻的上限后，对原样品重新确认即可正常判定。
func TestConfirmRejectsLimitEffectiveOneNanosecondAfterSampling(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	futureEff := time.Date(2026, 9, 10, 12, 0, 0, 500000000, time.UTC)
	sampledAt := futureEff.Add(-time.Nanosecond) // 采样比唯一版本生效早一纳秒
	mustLimit(t, s, "P1", "浊度", 5.0, futureEff)
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0)) // pH 有适用上限，本来能判定

	mustSample(t, s, "S1", "P1", sampledAt,
		Measurement{Item: "浊度", Value: 7}, Measurement{Item: "pH", Value: 7})

	got, err := s.Confirm("S1")
	if !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("唯一版本晚于采样一纳秒应整次拒绝, got sample=%+v err=%v", got, err)
	}
	if got.ID != "" || got.Status != "" {
		t.Fatalf("拒绝时返回的必须是空样品: %+v", got)
	}

	// 原样品继续待判定：没有逐项结果，为假的超标标记只是零值而非达标结论，
	// 采样时刻（含小数秒）与测量值保持原样。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	kept := list[0]
	if kept.Status != StatusPending || kept.Results != nil || kept.Exceeded {
		t.Fatalf("被拒绝的确认不得留下逐项结果或整份结论: %+v", kept)
	}
	if !kept.SampledAt.Equal(sampledAt) || kept.SampledAt.Nanosecond() != sampledAt.Nanosecond() {
		t.Fatalf("采样时刻丢失小数秒或被改动: %v", kept.SampledAt)
	}
	if len(kept.Measurements) != 2 {
		t.Fatalf("测量值被改动: %+v", kept.Measurements)
	}
	if _, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("没有已确认样品时应明确无结果 ok=%v err=%v", ok, err)
	}

	// 补录一版恰好等于采样时刻生效的上限后，对原样品重新确认：
	// 采用该版上限 10，浊度 7 达标，而不是借用未来版本的 5 或按零处理。
	mustLimit(t, s, "P1", "浊度", 10.0, sampledAt)
	conf, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("补录后重新确认: %v", err)
	}
	if conf.Status != StatusConfirmed || conf.Exceeded {
		t.Fatalf("补录后应确认且整份达标: %+v", conf)
	}
	rs := map[string]ItemResult{}
	for _, r := range conf.Results {
		rs[r.Item] = r
	}
	if turb := rs["浊度"]; turb.Limit != 10.0 || !turb.LimitEffective.Equal(sampledAt) || turb.Exceeded {
		t.Fatalf("浊度应采用补录的上限 10 且达标: %+v", turb)
	}
	if ph := rs["pH"]; ph.Limit != 8.0 || ph.Exceeded {
		t.Fatalf("pH 判定不应受浊度缺限影响: %+v", ph)
	}
}

// 恰好生效的边界上，采样时间和限值生效时间可以用不同时区表示同一真实时刻：
// 仍应采用新版本并保存同一时刻的依据，时区写法不能改变达标或超标结果。
func TestConfirmExactEffectiveAcrossTimezones(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	cst := time.FixedZone("CST", 8*3600)
	oldEff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// 新上限 5 的生效时刻用 +08:00 表示：与 UTC 2026-09-10 12:00:00.5 是同一瞬间
	newEffCST := time.Date(2026, 9, 10, 20, 0, 0, 500000000, cst)
	mustLimit(t, s, "P1", "浊度", 10.0, oldEff)
	mustLimit(t, s, "P1", "浊度", 5.0, newEffCST)

	// 两份样品采样于同一真实时刻（恰好等于新上限生效时刻），
	// 一份用 UTC 表示、一份用 +08:00 表示，时区写法不能改变判定结果。
	sampledUTC := time.Date(2026, 9, 10, 12, 0, 0, 500000000, time.UTC)
	sampledCST := time.Date(2026, 9, 10, 20, 0, 0, 500000000, cst)
	if !sampledUTC.Equal(sampledCST) || !sampledUTC.Equal(newEffCST) {
		t.Fatal("test setup: 应为同一真实时刻")
	}
	mustSample(t, s, "S-utc", "P1", sampledUTC, Measurement{Item: "浊度", Value: 7})
	mustSample(t, s, "S-cst", "P1", sampledCST, Measurement{Item: "浊度", Value: 7})

	for _, id := range []string{"S-utc", "S-cst"} {
		conf, err := s.Confirm(id)
		if err != nil {
			t.Fatalf("Confirm(%q): %v", id, err)
		}
		if !conf.Exceeded || len(conf.Results) != 1 {
			t.Fatalf("%s: 恰好生效应采用新版上限 5 并超标: %+v", id, conf)
		}
		r := conf.Results[0]
		if r.Limit != 5.0 || !r.Exceeded {
			t.Fatalf("%s: 应采用恰好生效的新版上限 5: %+v", id, r)
		}
		// 保存的依据与 +08:00 登记的生效时刻是同一真实时刻，小数秒不丢失
		if !r.LimitEffective.Equal(newEffCST) || r.LimitEffective.Nanosecond() != 500000000 {
			t.Fatalf("%s: 保存的生效时刻应为同一瞬间: %v", id, r.LimitEffective)
		}
		if !conf.SampledAt.Equal(sampledUTC) || conf.SampledAt.Nanosecond() != 500000000 {
			t.Fatalf("%s: 采样时刻丢失小数秒或被改动: %v", id, conf.SampledAt)
		}
	}

	// 按点查询：两份样品同一时刻采样，按编号升序；判定依据与时区写法无关
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if list[0].ID != "S-cst" || list[1].ID != "S-utc" {
		t.Fatalf("同时刻应按编号升序: %+v", list)
	}
	for _, smp := range list {
		if !smp.Exceeded || smp.Results[0].Limit != 5.0 ||
			!smp.Results[0].LimitEffective.Equal(newEffCST) {
			t.Fatalf("按点查询的判定依据与时区写法无关: %+v", smp)
		}
	}
}
