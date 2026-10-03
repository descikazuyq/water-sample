// 这份程序对应 README“使用”一节的完整示例：
// 一份含两个测量项目的样品，因其中一个项目在采样时刻没有适用上限而被拒绝确认；
// 补录一版不晚于采样时刻生效的上限后，对原样品重新确认并保存判定结果。
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

// printSamples 按“项目 / 测量值 / 实际使用的上限及其生效时间”打印，
// 判定依据不能只看一个超标布尔值。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（还没有判定依据）")
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

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo -dir ./water-data-demo")
	}

	// 1) 打开调用方指定的本地数据目录（空目录会自动创建；关闭后可重新打开）。
	store, err := water.Open(*dir)
	must("打开数据存放", err)

	const pointID = "P1"
	const sampleID = "S-20260910-01"
	sampledAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	phEffective := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) // 早于采样时间
	tFuture := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)    // 晚于采样时间
	tBackfill := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)  // 恰好等于采样时间

	// 2) 登记采样点。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)

	// 3) 登记限值：pH 有一版采样前已生效的上限；浊度“只”登记了一版
	//    采样之后才生效的上限——它存在，但这次样品用不了。
	_, err = store.SetLimit(pointID, "pH", 8.0, phEffective)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "浊度", 3.0, tFuture)
	must("登记浊度未来版本上限", err)

	// 4) 录入含两个测量项目的样品：pH 9（大于 8），浊度 4。初始状态为待判定。
	submitted, err := store.SubmitSample(sampleID, pointID, sampledAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "浊度", Value: 4},
	)
	must("录入样品", err)
	fmt.Printf("样品已录入：%s，状态 %s\n", submitted.ID, statusText(submitted.Status))

	// 5) 尝试确认。浊度在采样时刻没有“生效时间不晚于采样时间”的上限，
	//    本次确认被整体拒绝。
	rejected, err := store.Confirm(sampleID)
	if err == nil {
		log.Fatal("缺少适用上限时确认不应成功")
	}
	// 必须显式识别错误：不能忽略 err 后把返回的空样品当成成功结果。
	// err != nil 时 rejected 是零值（连编号、待判定状态都没有），不能使用。
	if !errors.Is(err, water.ErrMissingLimit) {
		log.Fatalf("预期缺少适用上限错误，实际为: %v", err)
	}
	fmt.Printf("确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作判定结果）\n",
		rejected.ID, rejected.Status)

	// 6) 按采样点查看：样品仍是“待判定”，没有任何逐项结果；
	//    超标标记 false 只是零值，含义是“没有结论”，不是“达标”。
	list, err := store.ListByPoint(pointID)
	must("按采样点查询", err)
	fmt.Println("被拒绝后按采样点查询：")
	printSamples(list)
	if list[0].Status != water.StatusPending || len(list[0].Results) != 0 {
		log.Fatal("被拒绝的确认不得留下单项结果或改变待判定状态")
	}

	// 7) 为缺失的浊度补录一版上限，生效时间恰好等于采样时间。
	//    生效时间不晚于采样时间即可采用，恰好相等时采用的正是这一版。
	_, err = store.SetLimit(pointID, "浊度", 4.0, tBackfill)
	must("补录浊度上限", err)

	// 8) 对“原来那份待判定样品”重新确认（样品编号不变，不需要重新录入）。
	confirmed, err := store.Confirm(sampleID)
	must("补录后重新确认", err)
	fmt.Println("补录后重新确认成功，已保存的判定依据：")
	printSamples([]water.Sample{confirmed})
	if confirmed.Status != water.StatusConfirmed {
		log.Fatalf("样品状态应为已确认，实际为 %s", confirmed.Status)
	}
	overall := "达标"
	if confirmed.Exceeded {
		overall = "超标"
	}
	fmt.Printf("整份样品结论：%s（一项超标即整份超标；等于上限算达标）\n", overall)

	// 9) 再次按采样点查询，与失败时的待判定记录对照：状态已是已确认，
	//    并带上了每个项目实际使用的上限和生效时间。
	list, err = store.ListByPoint(pointID)
	must("确认后按采样点查询", err)
	fmt.Println("确认成功后按采样点查询：")
	printSamples(list)

	// 10) 关闭后重新打开同一目录，记录仍在（不同目录的数据互不混用）。
	must("关闭数据存放", store.Close())
	reopened, err := water.Open(*dir)
	must("重新打开数据存放", err)
	defer func() {
		if err := reopened.Close(); err != nil {
			log.Printf("关闭重新打开的数据存放: %v", err)
		}
	}()
	list, err = reopened.ListByPoint(pointID)
	must("重新打开后按采样点查询", err)
	fmt.Println("关闭后重新打开同一目录，记录仍在：")
	printSamples(list)
}
