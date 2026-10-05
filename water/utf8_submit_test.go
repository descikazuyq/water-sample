package water

import (
	"errors"
	"testing"
)

// 含无效 UTF-8 字节的编号、采样点编号或项目名必须整份拒绝，
// 返回空样品和 ErrInvalidEncoding，不能落入其它错误分支或重复录入成功。
func TestSubmitSampleInvalidUTF8(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 7, at(1, 0))

	bad := string([]byte{'p', 0xFF})
	cases := []struct {
		name  string
		id    string
		point string
		item  string
	}{
		{"样品编号含孤立 0xFF", bad, "P1", "pH"},
		{"样品编号多字节序列不完整", string([]byte{'S', 0xE4, 0xB8}), "P1", "pH"},
		{"采样点编号含孤立 0xFE", "S1", string([]byte{'P', 0xFE}), "pH"},
		{"项目名含孤立 0xFF", "S1", "P1", bad},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			smp, err := s.SubmitSample(c.id, c.point, at(2, 0), Measurement{Item: c.item, Value: 6})
			if !errors.Is(err, ErrInvalidEncoding) {
				t.Fatalf("expected ErrInvalidEncoding, got smp=%+v err=%v", smp, err)
			}
			if smp.ID != "" || smp.PointID != "" || smp.Status != "" || smp.Measurements != nil {
				t.Fatalf("rejected submit must return empty sample, got %+v", smp)
			}
		})
	}
}

// 非法项目出现在最后、其余项目和测量值全部合法时也不能留下部分记录；
// 拒绝不能占用样品编号，改成合法文本后应能正常录入。
func TestSubmitSampleInvalidUTF8Atomic(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 7, at(1, 0))
	mustLimit(t, s, "P1", "浊度", 5, at(1, 0))

	badItem := string([]byte{0xE4, 0xB8}) // 不完整的“中”
	if _, err := s.SubmitSample("S1", "P1", at(2, 0),
		Measurement{Item: "pH", Value: 6},
		Measurement{Item: badItem, Value: 1},
	); !errors.Is(err, ErrInvalidEncoding) {
		t.Fatalf("expected ErrInvalidEncoding, got %v", err)
	}

	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("rejected submit must leave no record, got %+v", list)
	}

	// 同一编号改成合法文本后正常录入，先前的失败内容不被当成已有样品。
	smp, err := s.SubmitSample("S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 6})
	if err != nil {
		t.Fatalf("resubmit with valid text: %v", err)
	}
	if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusPending {
		t.Fatalf("unexpected sample %+v", smp)
	}
}

// 用已确认样品的编号提交非法文本：报编码错误而非内容冲突或重复录入成功，
// 原测量值、状态、逐项限值及生效时间、整份结论保持原样。
func TestSubmitSampleInvalidUTF8OnConfirmedSample(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 7, at(1, 0))
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: "pH", Value: 8})
	before, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	bad := string([]byte{'S', 0xFF})
	if _, err := s.SubmitSample(bad, "P1", at(2, 0), Measurement{Item: "pH", Value: 8}); !errors.Is(err, ErrInvalidEncoding) {
		t.Fatalf("expected ErrInvalidEncoding, got %v", err)
	}
	// 同一编号、非法项目名：不能走同内容返回原样品的分支。
	if _, err := s.SubmitSample("S1", "P1", at(2, 0), Measurement{Item: string([]byte{0xFE}), Value: 8}); !errors.Is(err, ErrInvalidEncoding) {
		t.Fatalf("expected ErrInvalidEncoding, got %v", err)
	}

	after, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm after rejected submit: %v", err)
	}
	if after.Status != StatusConfirmed || after.Exceeded != before.Exceeded ||
		len(after.Results) != 1 || after.Results[0] != before.Results[0] ||
		len(after.Measurements) != 1 || after.Measurements[0] != before.Measurements[0] {
		t.Fatalf("confirmed sample changed: before=%+v after=%+v", before, after)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: ok=%v err=%v", ok, err)
	}
	if latest.ID != "S1" {
		t.Fatalf("LatestResult changed, got %+v", latest)
	}
}

// “�”本身是合法字符，中文与有效多字节字符照常接受，
// 且返回记录、按点查看与落盘数据中的文本保持一致。
func TestSubmitSampleValidUTF8Accepted(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH�", 7, at(1, 0))
	mustLimit(t, s, "P1", "浊度", 5, at(1, 0))

	smp, err := s.SubmitSample("样品�1", "P1", at(2, 0),
		Measurement{Item: "pH�", Value: 6},
		Measurement{Item: "浊度", Value: 4},
	)
	if err != nil {
		t.Fatalf("valid UTF-8 (incl. U+FFFD) must be accepted: %v", err)
	}
	if smp.ID != "样品�1" {
		t.Fatalf("id not preserved: %q", smp.ID)
	}

	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "样品�1" {
		t.Fatalf("ListByPoint inconsistent: %+v err=%v", list, err)
	}

	// 重新打开后按原编号仍能找到，文本未被替换。
	s.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Confirm("样品�1"); err != nil {
		t.Fatalf("Confirm after reopen: %v", err)
	}
	list2, err := s2.ListByPoint("P1")
	if err != nil || len(list2) != 1 || list2[0].ID != "样品�1" || list2[0].Measurements[0].Item != "pH�" {
		t.Fatalf("persisted text inconsistent: %+v err=%v", list2, err)
	}
}

// 全部文本合法时沿用现有校验：去首尾空白后相同的项目名仍属重复。
func TestSubmitSampleValidTextKeepsExistingValidation(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")

	if _, err := s.SubmitSample("S1", "P1", at(2, 0),
		Measurement{Item: "pH", Value: 6},
		Measurement{Item: " pH ", Value: 6},
	); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("expected ErrDuplicateItem, got %v", err)
	}
	if _, err := s.SubmitSample("S1", "P9", at(2, 0), Measurement{Item: "pH", Value: 6}); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("expected ErrUnknownPoint, got %v", err)
	}
}
