package tables

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFormulaPrecedenceUnaryAndLeadingEquals(t *testing.T) {
	expr, err := CompileFormula("=1 + 2 * 3", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	got, err := expr.Evaluate(func(string) (any, error) { return nil, nil })
	if err != nil || got != 7.0 {
		t.Fatalf("got=%v err=%v, want 7", got, err)
	}

	grouped, err := CompileFormula("(1 + 2) * -3", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	got, err = grouped.Evaluate(func(string) (any, error) { return nil, nil })
	if err != nil || got != -9.0 {
		t.Fatalf("got=%v err=%v, want -9", got, err)
	}

	neg, err := CompileFormula("!FALSE", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	got, err = neg.Evaluate(func(string) (any, error) { return nil, nil })
	if err != nil || got != true {
		t.Fatalf("got=%v err=%v, want true", got, err)
	}
}

func TestFormulaFieldRefsRenameAndSerdeASTCompatibility(t *testing.T) {
	expr, err := CompileFormula("{单价} * {数量}", func(name string) (string, bool) {
		switch name {
		case "单价":
			return "f-price", true
		case "数量":
			return "f-qty", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	expr.Refs(&refs)
	if !reflect.DeepEqual(refs, []string{"f-price", "f-qty"}) {
		t.Fatalf("refs=%v", refs)
	}
	rendered := expr.Source(func(id string) (string, bool) {
		switch id {
		case "f-price":
			return "最新单价", true
		case "f-qty":
			return "件数", true
		default:
			return "", false
		}
	})
	if rendered != "({最新单价} * {件数})" {
		t.Fatalf("rendered=%q", rendered)
	}

	// Verify JSON AST round-trips identically to Rust serde externally-tagged enum format.
	raw, err := json.Marshal(expr)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"Binary":["Multiply",{"Field":"f-price"},{"Field":"f-qty"}]}`
	if string(raw) != wantJSON {
		t.Fatalf("ast json = %s, want %s", raw, wantJSON)
	}
	var decoded FormulaExpr
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	val, err := decoded.Evaluate(func(id string) (any, error) {
		switch id {
		case "f-price":
			return 12.5, nil
		case "f-qty":
			return 4.0, nil
		default:
			return nil, nil
		}
	})
	if err != nil || val != 50.0 {
		t.Fatalf("val=%v err=%v, want 50", val, err)
	}
}

func TestFormulaFunctionsAndLookupArrays(t *testing.T) {
	expr, err := CompileFormula(
		"IF(SUM({nums}) >= 10, ARRAYJOIN({tags}, ' / '), CONCATENATE('low:', COUNT({nums})))",
		func(name string) (string, bool) {
			switch name {
			case "nums":
				return "f-nums", true
			case "tags":
				return "f-tags", true
			default:
				return "", false
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	high, err := expr.Evaluate(func(id string) (any, error) {
		switch id {
		case "f-nums":
			return []any{0.0, 4.0, nil, 6.0}, nil
		case "f-tags":
			return []any{"A", "B"}, nil
		default:
			return nil, nil
		}
	})
	if err != nil || high != "A / B" {
		t.Fatalf("high=%v err=%v", high, err)
	}

	low, err := expr.Evaluate(func(id string) (any, error) {
		switch id {
		case "f-nums":
			return []any{0.0, false, nil, 3.0}, nil
		case "f-tags":
			return []any{}, nil
		default:
			return nil, nil
		}
	})
	if err != nil || low != "low:2" {
		t.Fatalf("low=%v err=%v", low, err)
	}

	stats, err := CompileFormula("AVERAGE({nums}) + MIN({nums}) + MAX({nums})", func(name string) (string, bool) {
		return "f-nums", name == "nums"
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := stats.Evaluate(func(string) (any, error) { return []any{2.0, 4.0, 6.0}, nil })
	if err != nil || got != 12.0 {
		t.Fatalf("stats=%v err=%v", got, err)
	}

	emptyAvg, err := CompileFormula("AVERAGE({nums})", func(string) (string, bool) { return "f-nums", true })
	if err != nil {
		t.Fatal(err)
	}
	got, err = emptyAvg.Evaluate(func(string) (any, error) { return []any{}, nil })
	if err != nil || got != nil {
		t.Fatalf("emptyAvg=%v err=%v, want nil", got, err)
	}
}

func TestFormulaRejectsInvalidSyntaxAndDivZero(t *testing.T) {
	bad := []string{"", "1 +", "(1 + 2", "1 2", "{missing}", "UNKNOWN(1)", "IF(TRUE, 1)", "'unterminated", "{unclosed"}
	for _, src := range bad {
		if _, err := CompileFormula(src, func(string) (string, bool) { return "", false }); err == nil {
			t.Fatalf("expected error for %q", src)
		}
	}
	if _, err := CompileFormula("ARRAYJOIN({a}, ',', 'extra')", func(string) (string, bool) { return "a", true }); err == nil {
		t.Fatal("expected error for extra ARRAYJOIN arg")
	}
	divZero, err := CompileFormula("1 / 0", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := divZero.Evaluate(func(string) (any, error) { return nil, nil }); err == nil || err.Error() != "公式不能除以零" {
		t.Fatalf("divZero err = %v", err)
	}
}
