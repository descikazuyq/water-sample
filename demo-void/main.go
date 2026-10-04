// 这份程序对应 README“样品作废”一节的完整示例：
// 同一采样点上两份已确认样品——较早的一份达标，较晚的一份至少一项超标；
// 作废较晚样品后，按点列表仍保留它的原测量与逐项判定依据（仅供核对历史），
// 最近有效结果则退到较早的已确认样品。另一个采样点只有一份未确认即作废的
// 待判定样品，用来说明作废不会凭空生成结论，且没有其他有效样品时
// 最近有效结果明确为“无结果”，不能把空返回或为假的超标标记解释成达标。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/descikazuyq/water-sample/water"
)

const layout = "2006-01-02 15:04:05 Z07:00"

func must(action string, err error) {
	if err != nil {
		log.Fatalf("%s 失败: %v", action, err)
	}
}

func statusText(st water.Status) string {
	switch st {
	case water.StatusPending:
		return "待判定"
	case water.StatusConfirmed:
		return "已确认"
	case water.StatusVoided:
		return "已作废"
	default:
		return string(st)
	}
}

// printSamples 打印状态、超标标记、作废原因以及逐项判定依据。
// 作废记录的历史依据和有效样品的当前判定用的是同一套字段，
// 读者需要结合“状态”区分两者，不能只看超标标记。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if smp.VoidReason != "" {
			fmt.Printf("    作废原因：%s\n", smp.VoidReason)
		}
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（没有已保存的判定依据）")
			continue
		}
		for _, r := range smp.Results {
			verdict := "达标"
			if r.Exceeded {
				verdict = "超标"
			}
			fmt.Printf("    逐项判定：项目 %s | 测量值 %g | 适用上限 %g（%s 生效）| %s\n",
				r.Item, r.Value, r.Limit, r.LimitEffective.UTC().Format(layout), verdict)
		}
	}
}

// printLatest 打印最近有效结果；没有已确认且未作废的样品时明确打印“无结果”，
// 不把空样品或记录里为假的超标标记解释成达标。
func printLatest(store *water.Store, pointID string) {
	latest, ok, err := store.LatestResult(pointID)
	must("查询最近有效结果", err)
	if !ok {
		fmt.Printf("  采样点 %s 的最近有效结果：无（没有已确认且未作废的样品；返回空样品编号 %q、状态 %q）\n",
			pointID, latest.ID, latest.Status)
		fmt.Println("  注意：这既不是达标也不是超标，不能把空返回或待判定记录的超标标记 false 当成达标。")
		return
	}
	fmt.Printf("  采样点 %s 的最近有效结果：\n", pointID)
	printSamples([]water.Sample{latest})
}

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo-void -dir ./water-data-void-demo")
	}

	store, err := water.Open(*dir)
	must("打开数据存放", err)
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭数据存放: %v", err)
		}
	}()

	const pointID = "P1"
	const earlierID = "S-20260905-01"
	const laterID = "S-20260910-01"
	eff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	earlierAt := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	laterAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// 1) 登记采样点和两版上限：pH 8、COD 30，均在 2026-09-01 生效。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)
	_, err = store.SetLimit(pointID, "pH", 8.0, eff)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "COD", 30.0, eff)
	must("登记 COD 上限", err)

	// 2) 较早样品（9/5 采样）：pH 7、COD 20，两项都不超标。
	_, err = store.SubmitSample(earlierID, pointID, earlierAt,
		water.Measurement{Item: "pH", Value: 7},
		water.Measurement{Item: "COD", Value: 20},
	)
	must("录入较早样品", err)
	earlier, err := store.Confirm(earlierID)
	must("确认较早样品", err)

	// 3) 较晚样品（9/10 采样）：pH 9 大于上限 8（超标），
	//    COD 30 恰好等于上限 30（达标），任一项目超标则整份样品超标。
	_, err = store.SubmitSample(laterID, pointID, laterAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "COD", Value: 30},
	)
	must("录入较晚样品", err)
	later, err := store.Confirm(laterID)
	must("确认较晚样品", err)
	fmt.Println("两份样品均已确认（按采样时间从晚到早排列）：")
	printSamples([]water.Sample{later, earlier})

	// 4) 作废前，最近有效结果指向采样时间最晚的已确认样品，即较晚的超标样品。
	fmt.Println("作废前查询最近有效结果：")
	printLatest(store, pointID)

	// 5) 作废原因去掉首尾空白后不能为空。仅含空白的原因直接拒绝，
	//    返回空样品——先处理 err，不能把空样品当作作废成功。
	blank, err := store.Void(laterID, "   ")
	if err == nil {
		log.Fatal("作废原因仅含空白时不应成功")
	}
	if !errors.Is(err, water.ErrEmptyField) {
		log.Fatalf("预期空白字段错误，实际为: %v", err)
	}
	fmt.Printf("空白原因作废被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作作废成功）\n",
		blank.ID, blank.Status)

	// 6) 填写非空原因（两端空白会被去掉）后作废较晚样品。
	voided, err := store.Void(laterID, "  复测确认样品污染  ")
	must("作废较晚样品", err)
	fmt.Println("填写非空原因后作废成功，返回的作废记录：")
	printSamples([]water.Sample{voided})

	// 7) 按点列表：两份样品都在。较晚样品状态为已作废并带原因，
	//    作废前保存的测量值、所用上限、生效时间和逐项结论原样保留。
	list, err := store.ListByPoint(pointID)
	must("作废后按采样点查询", err)
	fmt.Println("作废后按采样点查询（作废记录仍列出，历史依据保留，仅供核对历史）：")
	printSamples(list)

	// 8) 最近有效结果跳过已作废样品，退到较早的达标样品；
	//    返回内容整体来自较早样品，不能混入较晚样品的 pH 9 等任何数据。
	fmt.Println("作废后查询最近有效结果（退到较早的已确认样品）：")
	printLatest(store, pointID)

	// 9) 对已作废样品再次确认：明确拒绝，返回空样品。
	reconfirmed, err := store.Confirm(laterID)
	if err == nil {
		log.Fatal("已作废样品再次确认不应成功")
	}
	if !errors.Is(err, water.ErrVoided) {
		log.Fatalf("预期样品已作废错误，实际为: %v", err)
	}
	fmt.Printf("对已作废样品再次确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作确认成功）\n",
		reconfirmed.ID, reconfirmed.Status)

	// 10) 拒绝不改变任何记录：作废状态、原因与历史依据继续保留。
	list, err = store.ListByPoint(pointID)
	must("拒绝后按采样点查询", err)
	fmt.Println("再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：")
	printSamples(list)
	var kept water.Sample
	for _, smp := range list {
		if smp.ID == laterID {
			kept = smp
		}
	}
	if kept.Status != water.StatusVoided || kept.VoidReason != "复测确认样品污染" ||
		!kept.Exceeded || len(kept.Results) != 2 {
		log.Fatalf("拒绝确认不得改变作废记录: %+v", kept)
	}

	// 11) 另一个采样点：一份录入后从未确认、直接作废的待判定样品。
	const point2ID = "P2"
	const pendingID = "S-20260912-01"
	pendingAt := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	_, err = store.RegisterPoint(point2ID, "二号取水口")
	must("登记第二个采样点", err)
	_, err = store.SetLimit(point2ID, "pH", 8.0, eff)
	must("登记第二个采样点的 pH 上限", err)
	pending, err := store.SubmitSample(pendingID, point2ID, pendingAt,
		water.Measurement{Item: "pH", Value: 7})
	must("录入待判定样品", err)
	fmt.Printf("另一份样品录入后不确认直接作废（录入时状态为 %s，尚无判定依据）：\n",
		statusText(pending.Status))
	voidedPending, err := store.Void(pendingID, "录入信息有误")
	must("作废待判定样品", err)
	printSamples([]water.Sample{voidedPending})
	if len(voidedPending.Results) != 0 || voidedPending.Exceeded {
		log.Fatalf("待判定样品作废后不应凭空获得结论: %+v", voidedPending)
	}

	// 12) 按点列表能查到这份作废记录，但它没有逐项判定；该点没有其他
	//     已确认且未作废的样品，最近有效结果明确表示“无结果”。
	list2, err := store.ListByPoint(point2ID)
	must("第二个采样点按点查询", err)
	fmt.Println("第二个采样点按点查询（作废记录可查，但没有逐项判定）：")
	printSamples(list2)
	fmt.Println("第二个采样点查询最近有效结果：")
	printLatest(store, point2ID)
}
