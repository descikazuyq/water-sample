package water

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“样品编号唯一性覆盖整份文件”这一层：
// 文件顶层可能写出多个会被当作 samples 读入的对象——重复写出的 samples 属性
// 会被 encoding/json 合并进同一个 map（同键后写覆盖先写），Samples、SAMPLES
// 等大小写写法也填同一个字段；后面再出现空对象或 null 还可能把前面的内容抹掉。
// 因此编号核对必须跨所有这些集合共用同一份“已见编号”，并且无论反序列化后的
// samples 是不是 nil 都要执行：同一编号分散在不同集合、两次出现之间隔着其他
// 样品或顶层字段、字段名换大小写或写成 Unicode 转义、编号一处直接写出另一处
// 转义、内容完全相同，都要在打开时整体拒绝。多个非空集合但编号互不冲突时仍
// 照常读入，不能仅因集合字段出现多次就一概拒绝；其他顶层字段（如 points、
// limits 或未知字段）里的同名键不能被误当成样品编号。

// rawTopLevel 用给定的顶层属性片段拼出整份数据文件（始终带一份正常的 points）。
func rawTopLevel(t *testing.T, segments ...string) string {
	t.Helper()
	pts, err := json.Marshal(openPoints())
	if err != nil {
		t.Fatalf("marshal points: %v", err)
	}
	return fmt.Sprintf(`{"points":%s,%s}`, pts, strings.Join(segments, ","))
}

// rawFieldObject 拼出 "键":{条目} 形式的顶层属性片段；keyToken 是文件中实际
// 写出的键标记，如 strconv.Quote("samples") 或字面量 `"samples"`。
func rawFieldObject(t *testing.T, keyToken string, entries []string) string {
	t.Helper()
	return keyToken + ":{" + strings.Join(entries, ",") + "}"
}

func rawSamplesField(t *testing.T, field string, entries []string) string {
	t.Helper()
	return rawFieldObject(t, strconv.Quote(field), entries)
}

func writeRawTopLevel(t *testing.T, dir string, segments ...string) {
	t.Helper()
	writeRawDataFile(t, dir, rawTopLevel(t, segments...))
}

func pendingSample(id string, day int) *Sample {
	return &Sample{
		ID: id, PointID: "P1", SampledAt: at(day, 0),
		Measurements: []Measurement{{Item: openPH, Value: 5}},
		Status:       StatusPending,
	}
}

// 任务核心场景的跨集合版本：第一个 samples 对象保存 S1 的已确认超标结论
// （9 > 8）和另一份样品，第二个 samples 对象又保存同编号 S1 的已确认达标
// 结论（7 ≤ 8）。不能让后者替换原结论：整次打开失败、nil 存放。
func TestOpenDuplicateSampleIDAcrossSeparateCollections(t *testing.T) {
	dir := t.TempDir()
	other := pendingSample("S-OTHER", 11)
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
			rawEntry(t, strconv.Quote("S-OTHER"), other),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
		}),
	)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")

	// 不留任何内部状态：把第二个集合改成不冲突的编号后，同目录即可正常打开，
	// 且读回的 S1 仍是超标结论（证明此前没有悄悄读入达标版本）。
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
			rawEntry(t, strconv.Quote("S-OTHER"), other),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 12)),
		}),
	)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("集合间编号互不冲突时应正常打开: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 3 {
		t.Fatalf("两个非空集合的样品都应读入: %+v err=%v", list, err)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向保留超标结论的 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	if !latest.Exceeded || latest.Results[0].Value != 9 || latest.Results[0].Limit != 8 {
		t.Fatalf("已确认的超标依据必须原样保留: %+v", latest)
	}
}

// 分散在两个集合里的同一编号，即使两条记录内容完全相同也按重复编号拒绝。
func TestOpenDuplicateSampleIDAcrossCollectionsIdenticalRecords(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		}),
	)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 编号按 JSON 解码后的完整文本判断：跨集合时一处直接写 "S1"、另一处把 S
// 写成 Unicode 转义（"S1"），仍是同一编号。
func TestOpenDuplicateSampleIDAcrossCollectionsUnicodeEscape(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, `"S1"`, dupExceededS1()),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, `"S1"`, dupCompliantS1()),
		}),
	)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 跨集合重复不依赖两处相邻：两个 samples 对象之间隔着其他顶层字段，
// 第二个集合里 S2 与 S1 之间还隔着 S2，仍要识别最先出现的重复。
func TestOpenDuplicateSampleIDAcrossCollectionsNonAdjacent(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
		}),
		`"limits":{}`,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
			rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
		}),
	)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 大小写不同的字段名写法都会被读取识别为同一个样品集合字段，不能借换大小写
// 绕过编号核对；字段名自身把字母写成 Unicode 转义（解码后仍是 samples）也算。
// 与 encoding/json 的 foldName 一样按 Unicode 折叠等价判定：长 s（U+017F，
// "ſamples"）也会折叠成 samples 并被读进同一字段，同样不能绕过。
func TestOpenDuplicateSampleIDAcrossCaseVariantFields(t *testing.T) {
	cases := []struct {
		name      string
		firstKey  string
		secondKey string
		secondTok string
	}{
		{"Samples 与 samples", "Samples", "samples", ""},
		{"SAMPLES 与 Samples", "SAMPLES", "Samples", ""},
		{"samples 与 SAMPLES", "samples", "SAMPLES", ""},
		{"字段名 Unicode 转义", "samples", "", `"samples"`},
		{"长 s 折叠写法 ſamples", "ſamples", "samples", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			second := rawSamplesField(t, c.secondKey, []string{
				rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
			})
			if c.secondTok != "" {
				second = rawFieldObject(t, c.secondTok, []string{
					rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
				})
			}
			writeRawTopLevel(t, dir,
				rawSamplesField(t, c.firstKey, []string{
					rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
				}),
				second,
			)
			assertOpenRejectsDuplicateSampleID(t, dir, "S1")
		})
	}
}

// 后面再次出现空集合或 null，也不能让前面已经出现的重复编号逃过核对：
// encoding/json 遇到后写的 null 会把整个 map 置空，若核对以反序列化结果
// 非空为前提就会漏掉前面的重复。
func TestOpenDuplicateSampleIDNotHiddenByNullOrEmptyCollection(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) []string
	}{
		{
			"跨集合重复后跟 null",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
					}),
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
					}),
					`"samples":null`,
				}
			},
		},
		{
			"跨集合重复后跟着空集合",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
					}),
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
					}),
					rawSamplesField(t, "samples", nil),
				}
			},
		},
		{
			"两个集合之间夹着 null",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
					}),
					`"samples":null`,
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
					}),
				}
			},
		},
		{
			"单集合内重复后跟 null",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
						rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
					}),
					`"samples":null`,
				}
			},
		},
		{
			"单集合内重复后跟空集合",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
						rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
					}),
					rawSamplesField(t, "samples", nil),
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawTopLevel(t, dir, c.make(t)...)
			assertOpenRejectsDuplicateSampleID(t, dir, "S1")
		})
	}
}

// 跨集合报告的重复编号按文件中的出现顺序稳定：第一个集合顺序为 S1、S2，
// 第二个集合顺序为 S2、S1，最先两次出现的编号是 S2，应报告 S2 而不是 S1。
func TestOpenDuplicateSampleIDAcrossCollectionsReportedInOccurrenceOrder(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
			rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
		}),
	)
	_, err := Open(dir)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "S2") {
		t.Fatalf("应按出现顺序报告最先重复的 S2，实际 %q", msg)
	}
	if strings.Contains(msg, "S1") {
		t.Fatalf("不应抢先报告尚未扫到第二次出现的 S1，实际 %q", msg)
	}
}

// 多个非空样品集合、编号互不冲突、其余内容合法时继续沿用现有读取行为：
// 不能仅因 samples 字段出现多次就一概拒绝，两个集合的样品都要读入。
func TestOpenMultipleNonEmptyCollectionsWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
		}),
		`"limits":{}`,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
		}),
	)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("多个非空集合并无重复编号时应正常打开: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两个集合的样品都应读入: %+v err=%v", list, err)
	}
}

// 空集合或 null 与其他集合混合、且没有重复编号时照常打开；大小写不同的字段
// 写法也共同构成同一份样品集合。
func TestOpenCollectionsMixedWithNullOrEmptyOrCaseVariants(t *testing.T) {
	cases := []struct {
		name      string
		segments  func(t *testing.T) []string
		wantCount int
	}{
		{
			"null 在前集合在后",
			func(t *testing.T) []string {
				return []string{
					`"samples":null`,
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
					}),
				}
			},
			1,
		},
		{
			"空集合在前非空在后",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "samples", nil),
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
					}),
				}
			},
			1,
		},
		{
			"大小写写法分别承载不同编号",
			func(t *testing.T) []string {
				return []string{
					rawSamplesField(t, "Samples", []string{
						rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
					}),
					rawSamplesField(t, "SAMPLES", []string{
						rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
					}),
				}
			},
			2,
		},
		{
			"字段名 Unicode 转义与普通写法共同构成集合",
			func(t *testing.T) []string {
				return []string{
					rawFieldObject(t, `"samples"`, []string{
						rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
					}),
					rawSamplesField(t, "samples", []string{
						rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
					}),
				}
			},
			2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawTopLevel(t, dir, c.segments(t)...)
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("无重复编号时应正常打开: %v", err)
			}
			defer s.Close()
			if list, err := s.ListByPoint("P1"); err != nil || len(list) != c.wantCount {
				t.Fatalf("应有 %d 份样品读入，got %+v err=%v", c.wantCount, list, err)
			}
		})
	}
}

// 不同样品使用同一个测量项目，即使分散在不同集合，也不算样品编号重复。
func TestOpenSameItemAcrossSeparateCollectionsIsNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
		}),
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S2"), pendingSample("S2", 11)),
		}),
	)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同样品各含 pH 是正常数据: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份不同编号的样品都应读入: %+v err=%v", list, err)
	}
}

// 不是样品集合的顶层字段（points、limits，或名称既不精确等于也不大小写
// 折叠为 samples 的其他字段）即使内部出现同名键，也不能被当成样品编号统计。
func TestOpenNonSamplesTopLevelFieldsAreNotCollections(t *testing.T) {
	dir := t.TempDir()
	writeRawTopLevel(t, dir,
		rawSamplesField(t, "samples", []string{
			rawEntry(t, strconv.Quote("S1"), pendingSample("S1", 10)),
		}),
		// 单数 sample 与带前后缀的字段都不匹配 samples。
		`"sample":{"S1":1}`,
		`"x-samples":{"S1":2}`,
	)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("非样品集合字段中的同名键不应导致编号重复: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("只应读入样品集合里的 S1: %+v err=%v", list, err)
	}
}
