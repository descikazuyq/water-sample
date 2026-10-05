package water

import (
	"errors"
	"math"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：SetLimit 只接受能作为合法 UTF-8 原样保存的
// 采样点编号和项目名。原始文本含孤立 0xFF、0xFE 字节或不完整的多字节序列时，
// 整次登记必须返回空限值记录和可被 errors.Is(err, water.ErrInvalidText) 识别的
// 文本编码错误：不新增任何限值版本、不占用生效时间、不改变已有上限，后续成功
// 操作也不能把失败请求保存进去。即使同次请求还存在采样点未登记、数值不是
// 有限数或生效时间缺失的问题，也必须报告编码错误。真正的 U+FFFD（“�”）、
// 中文以及文本内部的 U+0000 仍然接受；文本处理只去首尾空白。

// 非法文本出现在采样点编号或项目名时整次拒绝，返回空限值记录；编码问题
// 优先于采样点未登记、数值非有限数、生效时间缺失等其它一切校验。
func TestSetLimitRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")

	reject := func(label, point, item string, value float64, eff time.Time) {
		t.Helper()
		got, err := s.SetLimit(point, item, value, eff)
		assertEncodingError(t, err, label)
		if got.PointID != "" || got.Item != "" || got.Value != 0 || !got.Effective.IsZero() {
			t.Fatalf("%s: 失败必须返回空限值记录，实际为 %+v", label, got)
		}
	}

	// 项目名非法：孤立 0xFF、0xFE、截断的多字节字符
	reject("项目名含孤立 0xFF", "P1", "浊"+"\xff"+"度", 5, at(2, 0))
	reject("项目名含孤立 0xFE", "P1", "pH"+"\xfe", 5, at(2, 0))
	reject("项目名为截断的三字节序列", "P1", "浊度"+"\xe4\xb8", 5, at(2, 0))
	reject("项目名开头不完整序列", "P1", "\xe4\xb8"+"pH", 5, at(2, 0))
	// 采样点编号非法，即使该编号看起来未登记也不能报未知采样点
	reject("采样点含孤立 0xFF", "P1"+"\xff", "pH", 5, at(2, 0))
	reject("采样点与项目名都非法", "P9"+"\xfe", "pH"+"\xff", 5, at(2, 0))

	// 编码问题优先于其它一切校验：未知采样点、非有限数值、缺失生效时间
	reject("非法项目+采样点未登记", "P9", "pH"+"\xff", 5, at(2, 0))
	reject("非法项目+NaN", "P1", "pH"+"\xfe", math.NaN(), at(2, 0))
	reject("非法项目+Inf", "P1", "pH"+"\xff", math.Inf(1), at(2, 0))
	reject("非法采样点+生效时间缺失", "P1"+"\xff", "pH", 5, time.Time{})
	reject("非法项目+全部其它问题", "P9", "浊"+"\xff"+"度", math.Inf(-1), time.Time{})

	// 全部拒绝之后：没有任何限值版本留下，生效时间没有被占用，
	// 落盘文件中没有替换字符
	if len(s.limits) != 0 {
		t.Fatalf("被拒绝的登记不得留下限值版本，limits=%v", s.limits)
	}
	assertNoReplacementBytes(t, dir, "全部拒绝后")

	// 曾被失败请求使用的（采样点，项目，生效时间）组合应能正常首次登记
	lim, err := s.SetLimit("P1", "pH", 8.0, at(2, 0))
	if err != nil {
		t.Fatalf("失败请求不得占用生效时间: %v", err)
	}
	if lim.PointID != "P1" || lim.Item != "pH" || lim.Value != 8.0 {
		t.Fatalf("合法登记返回内容异常: %+v", lim)
	}
}

// 已有合法上限的项目遭遇非法文本登记：必须直接失败，已有上限不变；
// 关闭后重新打开同一数据目录，采样时间晚于两次生效时间的样品仍按
// 已成功登记的上限判定，不能被失败请求变成超标。
func TestSetLimitInvalidUTF8DoesNotTouchExistingLimit(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	// 合法项目名含真正的 U+FFFD，已有一版上限 10
	mustLimit(t, s, "P1", "浊�度", 10.0, at(1, 0))

	// 用“浊”加孤立 0xFF 字节再加“度”作项目名，登记较晚生效的上限 5：必须失败
	got, err := s.SetLimit("P1", "浊"+"\xff"+"度", 5.0, at(5, 0))
	assertEncodingError(t, err, "非法项目名登记较晚上限")
	if got.PointID != "" || got.Item != "" || got.Value != 0 || !got.Effective.IsZero() {
		t.Fatalf("失败必须返回空限值记录，实际为 %+v", got)
	}

	// 内存中只有原来一版上限
	versions := s.limits[limitGroup{pointID: "P1", item: "浊�度"}]
	if len(versions) != 1 || versions[0].Value != 10.0 {
		t.Fatalf("已有上限被失败请求改变: %+v", versions)
	}

	// 失败请求占用的生效时间仍可用于合法登记（此处先不登记，直接验证判定）
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重新打开同一数据目录，为“浊�度”录入采样时间晚于两次生效时间、
	// 测量值为 7 的样品：仍按已成功登记的上限 10 判为达标
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	mustSample(t, s2, "S1", "P1", at(10, 0), Measurement{Item: "浊�度", Value: 7})
	smp, err := s2.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(smp.Results) != 1 {
		t.Fatalf("判定结果异常: %+v", smp.Results)
	}
	r := smp.Results[0]
	if r.Item != "浊�度" || r.Limit != 10.0 || !r.LimitEffective.Equal(at(1, 0)) || r.Exceeded || smp.Exceeded {
		t.Fatalf("必须按已成功登记的上限 10 判为达标，实际为 %+v（整份 %+v）", r, smp)
	}
}

// 原本没有适用上限的项目：失败请求不能让该项目获得判定依据。
func TestSetLimitInvalidUTF8CreatesNoJudgmentBasis(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	// 非法项目名的登记失败，且同次请求还带着其它问题
	if _, err := s.SetLimit("P1", "浊"+"\xff"+"度", 5.0, at(1, 0)); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("应返回 ErrInvalidText，实际为 %v", err)
	}

	// 与该非法文本去空白后形近但合法的“浊度”项目仍没有适用上限
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "浊度", Value: 7})
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("失败请求不能留下判定依据，got %v", err)
	}
	// 失败请求用过的生效时间没有被占用：合法登记同一时刻应成功
	mustLimit(t, s, "P1", "浊度", 5.0, at(1, 0))
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("合法登记后应能确认: %v", err)
	}
	if smp.Results[0].Limit != 5.0 || smp.Exceeded != true {
		t.Fatalf("应按合法登记的上限 5 判定: %+v", smp.Results[0])
	}
}

// 真正的“�”（U+FFFD）、中文、文本内部的 U+0000 都是合法字符，继续接受；
// 只去首尾空白，不替换字符、不转换大小写、不合并不同名称；返回的限值与
// 后续判定中保存的项目名一致。相同生效时刻仍拒绝重复登记。
func TestSetLimitAcceptsValidUnicodeText(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")

	items := []string{
		"浊�度",     // 真正的替换字符 U+FFFD
		"浊度",      // 中文
		"溶氧🫧",     // 有效四字节字符
		"ph\x00x", // 内部 U+0000
		"pH",
		"PH", // 仅大小写不同，不得合并
	}
	for i, item := range items {
		lim, err := s.SetLimit("  P1  ", "  "+item+"  ", float64(10+i), at(1, i))
		if err != nil {
			t.Fatalf("合法文本 %q 不应被拒绝: %v", item, err)
		}
		if lim.PointID != "P1" || lim.Item != item {
			t.Fatalf("只应去掉首尾空白，实际为 %+v", lim)
		}
	}

	// 同一采样点同一项目的相同生效时刻仍拒绝重复登记
	if _, err := s.SetLimit("P1", "浊�度", 99.0, at(1, 0)); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("相同生效时刻应拒绝重复登记，got %v", err)
	}

	// 各项目各自登记、互不相同：若做归一化或合并，数量会对不上
	if len(s.limits) != len(items) {
		t.Fatalf("不同名称不得合并，limits=%v", s.limits)
	}

	// 后续判定中保存的项目名与登记时一致
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "浊�度", Value: 1})
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if smp.Results[0].Item != "浊�度" || smp.Results[0].Limit != 10.0 {
		t.Fatalf("判定中的项目名与限值应与登记一致: %+v", smp.Results[0])
	}

	// 重开后限值版本与项目名原样保存
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if len(s2.limits) != len(items) {
		t.Fatalf("重开后限值分组变化: %v", s2.limits)
	}
	for i, item := range items {
		versions := s2.limits[limitGroup{pointID: "P1", item: item}]
		if len(versions) != 1 || versions[0].Value != float64(10+i) || versions[0].Item != item {
			t.Fatalf("项目 %q 的限值在保存数据中缺失或被改写: %+v", item, versions)
		}
	}
}

// 已关闭的数据存放仍按原有规则拒绝操作，编码检查不改变这一点。
func TestSetLimitInvalidUTF8OnClosedStore(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.SetLimit("P1", "浊"+"\xff"+"度", 5.0, at(1, 0)); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
	if _, err := s.SetLimit("P1", "浊度", 5.0, at(1, 0)); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
}
