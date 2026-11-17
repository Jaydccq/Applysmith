package providerusage

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func number(v any) *float64 {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case string:
		var err error
		f, err = strconv.ParseFloat(n, 64)
		if err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil
	}
	return &f
}
func percent(v any) *float64 {
	f := number(v)
	if f == nil || *f > 100 {
		return nil
	}
	return f
}
func epoch(v any) time.Time {
	f := number(v)
	if f == nil || *f <= 0 || *f > 253402300799000 {
		return time.Time{}
	}
	return time.UnixMilli(int64(*f)).UTC().Truncate(time.Second)
}
func countdown(v any) time.Time {
	f := number(v)
	if f == nil || *f > 31*24*60*60*1000 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(*f) * time.Millisecond).Truncate(time.Second)
}
func resetString(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Second)
}
func parseMiniMax(data map[string]any) []Window {
	if code := number(object(data["base_resp"])["status_code"]); code != nil && *code != 0 {
		return nil
	}
	rows, _ := data["model_remains"].([]any)
	var out []Window
	for i, row := range rows {
		m := object(row)
		label := fmt.Sprintf("Model %d", i+1)
		if name, ok := m["model_name"].(string); ok {
			switch name {
			case "general":
				label = "Text"
			case "MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.5", "MiniMax-M2.1", "MiniMax-M2":
				label = name
			}
		}
		if left := percent(m["current_interval_remaining_percent"]); left != nil {
			used := 100 - *left
			reset := epoch(m["end_time"])
			if reset.IsZero() {
				reset = countdown(m["remains_time"])
			}
			out = append(out, Window{Name: label + " · 5 hours", UsedPercent: &used, ResetsAt: reset})
		}
		if left := percent(m["current_weekly_remaining_percent"]); left != nil {
			used := 100 - *left
			out = append(out, Window{Name: label + " · weekly", UsedPercent: &used, ResetsAt: countdown(m["weekly_remains_time"])})
		}
		if len(out) >= 32 {
			break
		}
	}
	return out
}
func parseGLM(data map[string]any) []Window {
	if data["success"] != true {
		return nil
	}
	if code := number(data["code"]); code != nil && *code != 0 && *code != 200 {
		return nil
	}
	rows, _ := object(data["data"])["limits"].([]any)
	var out []Window
	for _, row := range rows {
		m := object(row)
		typ, _ := m["type"].(string)
		if typ != "TOKENS_LIMIT" && typ != "CREDIT_LIMIT" && typ != "TIME_LIMIT" {
			continue
		}
		w := Window{Name: "Plan quota", Used: number(m["currentValue"]), Limit: number(m["usage"]), Remaining: number(m["remaining"]), UsedPercent: percent(m["percentage"]), ResetsAt: epoch(m["nextResetTime"])}
		if typ == "TIME_LIMIT" {
			w.Name = "Tool calls"
		} else {
			unit, n := number(m["unit"]), number(m["number"])
			if unit != nil && n != nil {
				minutes := map[float64]float64{1: 1440, 3: 60, 5: 1, 6: 10080}[*unit] * *n
				switch minutes {
				case 300:
					w.Name = "5 hours"
				case 10080:
					w.Name = "Weekly"
				default:
					w.Name = "Plan quota"
				}
			}
		}
		if w.Limit != nil && *w.Limit > 0 {
			if w.Used == nil && w.Remaining != nil && *w.Remaining <= *w.Limit {
				f := *w.Limit - *w.Remaining
				w.Used = &f
			}
			if w.Used != nil {
				p := math.Min(100, *w.Used / *w.Limit * 100)
				w.UsedPercent = &p
			}
		}
		if w.UsedPercent != nil {
			out = append(out, w)
		}
		if len(out) >= 32 {
			break
		}
	}
	return out
}
func parseOpenCode(data map[string]any, key bool) []Window {
	var out []Window
	if key {
		usage := object(data["usage"])
		for _, entry := range []struct{ field, name string }{{"rolling", "5 hours"}, {"weekly", "Weekly"}, {"monthly", "Monthly"}} {
			m := object(usage[entry.field])
			p := percent(m["percent"])
			if p != nil {
				out = append(out, Window{Name: entry.name, UsedPercent: p, ResetsAt: resetString(m["resetsAt"])})
			}
		}
	} else {
		meters := object(object(data["access"])["meters"])
		for _, entry := range []struct{ field, name string }{{"fiveHour", "5 hours"}, {"week", "Weekly"}, {"month", "Monthly"}} {
			m := object(meters[entry.field])
			used, limit := number(m["usedMicroCents"]), number(m["limitMicroCents"])
			if used != nil && limit != nil && *limit > 0 {
				p := math.Min(100, *used / *limit * 100)
				out = append(out, Window{Name: entry.name, UsedPercent: &p, ResetsAt: resetString(m["resetsAt"])})
			}
		}
	}
	return out
}
