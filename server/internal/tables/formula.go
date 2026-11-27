package tables

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFormulaSourceBytes = 4096
	maxFormulaNodes       = 256
	maxFormulaDepth       = 32
	maxFormulaTextBytes   = 32_768
)

// FormulaError represents a user-facing formula parse or evaluation error.
type FormulaError string

func (e FormulaError) Error() string { return string(e) }

// FormulaOp is a binary operator in a compiled formula AST.
type FormulaOp string

const (
	OpAdd          FormulaOp = "Add"
	OpSubtract     FormulaOp = "Subtract"
	OpMultiply     FormulaOp = "Multiply"
	OpDivide       FormulaOp = "Divide"
	OpConcat       FormulaOp = "Concat"
	OpEqual        FormulaOp = "Equal"
	OpNotEqual     FormulaOp = "NotEqual"
	OpLess         FormulaOp = "Less"
	OpLessEqual    FormulaOp = "LessEqual"
	OpGreater      FormulaOp = "Greater"
	OpGreaterEqual FormulaOp = "GreaterEqual"
)

func (op FormulaOp) syntax() (string, uint8) {
	switch op {
	case OpEqual:
		return "=", 1
	case OpNotEqual:
		return "!=", 1
	case OpLess:
		return "<", 1
	case OpLessEqual:
		return "<=", 1
	case OpGreater:
		return ">", 1
	case OpGreaterEqual:
		return ">=", 1
	case OpConcat:
		return "&", 2
	case OpAdd:
		return "+", 3
	case OpSubtract:
		return "-", 3
	case OpMultiply:
		return "*", 4
	case OpDivide:
		return "/", 4
	default:
		return "", 0
	}
}

func (op FormulaOp) valid() bool {
	_, prec := op.syntax()
	return prec > 0
}

// FormulaFunc is a supported built-in function name (stored uppercase in JSON AST).
type FormulaFunc string

const (
	FuncIf          FormulaFunc = "IF"
	FuncSum         FormulaFunc = "SUM"
	FuncAverage     FormulaFunc = "AVERAGE"
	FuncMin         FormulaFunc = "MIN"
	FuncMax         FormulaFunc = "MAX"
	FuncCount       FormulaFunc = "COUNT"
	FuncConcatenate FormulaFunc = "CONCATENATE"
	FuncArrayJoin   FormulaFunc = "ARRAYJOIN"
)

func parseFormulaFunc(name string, argc int) (FormulaFunc, error) {
	fn := FormulaFunc(strings.ToUpper(name))
	switch fn {
	case FuncIf:
		if argc != 3 {
			return "", FormulaError("IF 需要三个参数")
		}
		return fn, nil
	case FuncArrayJoin:
		if argc < 1 || argc > 2 {
			return "", FormulaError("ARRAYJOIN 需要一或两个参数")
		}
		return fn, nil
	case FuncSum, FuncAverage, FuncMin, FuncMax, FuncCount, FuncConcatenate:
		return fn, nil
	default:
		return "", FormulaError("公式中有不支持的函数")
	}
}

func (fn FormulaFunc) valid() bool {
	switch fn {
	case FuncIf, FuncSum, FuncAverage, FuncMin, FuncMax, FuncCount, FuncConcatenate, FuncArrayJoin:
		return true
	default:
		return false
	}
}

// ExprKind distinguishes AST node variants.
type ExprKind int

const (
	ExprLiteral ExprKind = iota + 1
	ExprField
	ExprUnary
	ExprBinary
	ExprCall
)

// FormulaExpr is a compiled formula AST node. Its JSON encoding matches Rust
// serde's externally-tagged enum format for seamless compatibility with
// existing formula configs stored in PostgreSQL.
type FormulaExpr struct {
	Kind    ExprKind
	Literal any
	FieldID string
	UnaryOp rune
	Inner   *FormulaExpr
	BinOp   FormulaOp
	Left    *FormulaExpr
	Right   *FormulaExpr
	Func    FormulaFunc
	Args    []FormulaExpr
}

func (e FormulaExpr) MarshalJSON() ([]byte, error) {
	switch e.Kind {
	case ExprLiteral:
		return marshalCompact(map[string]any{"Literal": e.Literal})
	case ExprField:
		return marshalCompact(map[string]any{"Field": e.FieldID})
	case ExprUnary:
		return marshalCompact(map[string]any{"Unary": []any{string(e.UnaryOp), e.Inner}})
	case ExprBinary:
		return marshalCompact(map[string]any{"Binary": []any{string(e.BinOp), e.Left, e.Right}})
	case ExprCall:
		args := e.Args
		if args == nil {
			args = []FormulaExpr{}
		}
		return marshalCompact(map[string]any{"Call": []any{string(e.Func), args}})
	default:
		return nil, errors.New("invalid formula expr kind")
	}
}

func (e *FormulaExpr) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 {
		return FormulaError("公式语法树无效")
	}
	for tag, payload := range raw {
		switch tag {
		case "Literal":
			var val any
			if err := json.Unmarshal(payload, &val); err != nil {
				return FormulaError("公式语法树无效")
			}
			*e = FormulaExpr{Kind: ExprLiteral, Literal: val}
			return nil
		case "Field":
			var id string
			if err := json.Unmarshal(payload, &id); err != nil {
				return FormulaError("公式语法树无效")
			}
			*e = FormulaExpr{Kind: ExprField, FieldID: id}
			return nil
		case "Unary":
			var tuple []json.RawMessage
			if err := json.Unmarshal(payload, &tuple); err != nil || len(tuple) != 2 {
				return FormulaError("公式语法树无效")
			}
			var opStr string
			if err := json.Unmarshal(tuple[0], &opStr); err != nil {
				return FormulaError("公式语法树无效")
			}
			r, size := utf8.DecodeRuneInString(opStr)
			if r == utf8.RuneError || size != len(opStr) {
				return FormulaError("公式语法树无效")
			}
			var inner FormulaExpr
			if err := json.Unmarshal(tuple[1], &inner); err != nil {
				return FormulaError("公式语法树无效")
			}
			*e = FormulaExpr{Kind: ExprUnary, UnaryOp: r, Inner: &inner}
			return nil
		case "Binary":
			var tuple []json.RawMessage
			if err := json.Unmarshal(payload, &tuple); err != nil || len(tuple) != 3 {
				return FormulaError("公式语法树无效")
			}
			var opStr string
			if err := json.Unmarshal(tuple[0], &opStr); err != nil {
				return FormulaError("公式语法树无效")
			}
			op := FormulaOp(opStr)
			if !op.valid() {
				return FormulaError("公式语法树无效")
			}
			var left, right FormulaExpr
			if err := json.Unmarshal(tuple[1], &left); err != nil {
				return FormulaError("公式语法树无效")
			}
			if err := json.Unmarshal(tuple[2], &right); err != nil {
				return FormulaError("公式语法树无效")
			}
			*e = FormulaExpr{Kind: ExprBinary, BinOp: op, Left: &left, Right: &right}
			return nil
		case "Call":
			var tuple []json.RawMessage
			if err := json.Unmarshal(payload, &tuple); err != nil || len(tuple) != 2 {
				return FormulaError("公式语法树无效")
			}
			var fnStr string
			if err := json.Unmarshal(tuple[0], &fnStr); err != nil {
				return FormulaError("公式语法树无效")
			}
			fn := FormulaFunc(fnStr)
			if !fn.valid() {
				return FormulaError("公式语法树无效")
			}
			var args []FormulaExpr
			if err := json.Unmarshal(tuple[1], &args); err != nil {
				return FormulaError("公式语法树无效")
			}
			*e = FormulaExpr{Kind: ExprCall, Func: fn, Args: args}
			return nil
		default:
			return FormulaError("公式语法树无效")
		}
	}
	return FormulaError("公式语法树无效")
}

type tokenKind int

const (
	tokLiteral tokenKind = iota + 1
	tokField
	tokName
	tokOp
	tokOpen
	tokClose
	tokComma
)

type formulaToken struct {
	kind    tokenKind
	literal any
	text    string
}

func lexFormula(source string) ([]formulaToken, error) {
	var tokens []formulaToken
	i := 0
	for i < len(source) {
		r, width := utf8.DecodeRuneInString(source[i:])
		i += width
		if unicode.IsSpace(r) {
			continue
		}
		var tok formulaToken
		switch {
		case r == '{':
			end := strings.IndexByte(source[i:], '}')
			if end < 0 {
				return nil, FormulaError("字段引用缺少右括号")
			}
			name := source[i : i+end]
			i += end + 1
			tok = formulaToken{kind: tokField, text: name}
		case r == '"' || r == '\'':
			quote := r
			var sb strings.Builder
			closed := false
			for i < len(source) {
				ch, w := utf8.DecodeRuneInString(source[i:])
				i += w
				if ch == quote {
					closed = true
					break
				}
				if ch == '\\' {
					if i >= len(source) {
						return nil, FormulaError("公式文本中的转义无效")
					}
					esc, ew := utf8.DecodeRuneInString(source[i:])
					i += ew
					switch esc {
					case 'n':
						sb.WriteByte('\n')
					case 'r':
						sb.WriteByte('\r')
					case 't':
						sb.WriteByte('\t')
					case '\\', '\'', '"':
						sb.WriteRune(esc)
					default:
						return nil, FormulaError("公式文本中的转义无效")
					}
				} else {
					sb.WriteRune(ch)
				}
			}
			if !closed {
				return nil, FormulaError("公式文本缺少结束引号")
			}
			tok = formulaToken{kind: tokLiteral, literal: sb.String()}
		case (r >= '0' && r <= '9') || r == '.':
			start := i - width
			for i < len(source) && ((source[i] >= '0' && source[i] <= '9') || source[i] == '.') {
				i++
			}
			num, err := strconv.ParseFloat(source[start:i], 64)
			if err != nil {
				return nil, FormulaError("公式数字格式无效")
			}
			fval, err := finiteNumber(num)
			if err != nil {
				return nil, err
			}
			tok = formulaToken{kind: tokLiteral, literal: fval}
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_':
			start := i - width
			for i < len(source) && (isASCIIAlnum(source[i]) || source[i] == '_') {
				i++
			}
			name := source[start:i]
			switch strings.ToUpper(name) {
			case "TRUE":
				tok = formulaToken{kind: tokLiteral, literal: true}
			case "FALSE":
				tok = formulaToken{kind: tokLiteral, literal: false}
			case "BLANK":
				tok = formulaToken{kind: tokLiteral, literal: nil}
			default:
				tok = formulaToken{kind: tokName, text: name}
			}
		case r == '(':
			tok = formulaToken{kind: tokOpen}
		case r == ')':
			tok = formulaToken{kind: tokClose}
		case r == ',':
			tok = formulaToken{kind: tokComma}
		case r == '+' || r == '-' || r == '*' || r == '/' || r == '&':
			tok = formulaToken{kind: tokOp, text: string(r)}
		case r == '=' || r == '<' || r == '>' || r == '!':
			hasEq := i < len(source) && source[i] == '='
			var op string
			switch {
			case r == '=':
				op = "="
			case r == '<' && !hasEq && i < len(source) && source[i] == '>':
				i++
				op = "!="
			case r == '<' && !hasEq:
				op = "<"
			case r == '>' && !hasEq:
				op = ">"
			case r == '!' && !hasEq:
				op = "!"
			case r == '<' && hasEq:
				op = "<="
			case r == '>' && hasEq:
				op = ">="
			case r == '!' && hasEq:
				op = "!="
			default:
				return nil, FormulaError("公式运算符无效")
			}
			if hasEq {
				i++
			}
			tok = formulaToken{kind: tokOp, text: op}
		default:
			return nil, FormulaError("公式中有无法识别的字符")
		}
		tokens = append(tokens, tok)
		if len(tokens) > maxFormulaNodes {
			return nil, FormulaError("公式过于复杂")
		}
	}
	return tokens, nil
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// CompileFormula parses a formula expression and resolves {field} references to field IDs.
func CompileFormula(source string, resolveField func(name string) (string, bool)) (FormulaExpr, error) {
	trimmed := strings.TrimSpace(source)
	trimmed = strings.TrimPrefix(trimmed, "=")
	if len(trimmed) > maxFormulaSourceBytes {
		return FormulaExpr{}, FormulaError("公式过长")
	}
	tokens, err := lexFormula(trimmed)
	if err != nil {
		return FormulaExpr{}, err
	}
	p := formulaParser{tokens: tokens, resolveField: resolveField}
	expr, err := p.expression(0, 0)
	if err != nil {
		return FormulaExpr{}, err
	}
	if p.pos < len(p.tokens) {
		return FormulaExpr{}, FormulaError("公式表达式未结束")
	}
	if err := expr.checkDepth(0); err != nil {
		return FormulaExpr{}, err
	}
	return expr, nil
}

type formulaParser struct {
	tokens       []formulaToken
	pos          int
	resolveField func(name string) (string, bool)
}

func (p *formulaParser) peek() (formulaToken, bool) {
	if p.pos >= len(p.tokens) {
		return formulaToken{}, false
	}
	return p.tokens[p.pos], true
}

func (p *formulaParser) next() (formulaToken, bool) {
	if p.pos >= len(p.tokens) {
		return formulaToken{}, false
	}
	tok := p.tokens[p.pos]
	p.pos++
	return tok, true
}

func (p *formulaParser) takeKind(k tokenKind) bool {
	if tok, ok := p.peek(); ok && tok.kind == k {
		p.pos++
		return true
	}
	return false
}

func (p *formulaParser) expression(minPrec uint8, depth int) (FormulaExpr, error) {
	if depth > maxFormulaDepth {
		return FormulaExpr{}, FormulaError("公式嵌套过深")
	}
	tok, ok := p.next()
	if !ok {
		return FormulaExpr{}, FormulaError("公式缺少表达式")
	}
	var lhs FormulaExpr
	switch tok.kind {
	case tokLiteral:
		lhs = FormulaExpr{Kind: ExprLiteral, Literal: tok.literal}
	case tokField:
		id, found := p.resolveField(tok.text)
		if !found {
			return FormulaExpr{}, FormulaError("公式引用了不存在的字段")
		}
		lhs = FormulaExpr{Kind: ExprField, FieldID: id}
	case tokOp:
		if tok.text != "+" && tok.text != "-" && tok.text != "!" {
			return FormulaExpr{}, FormulaError("公式缺少表达式")
		}
		inner, err := p.expression(5, depth+1)
		if err != nil {
			return FormulaExpr{}, err
		}
		lhs = FormulaExpr{Kind: ExprUnary, UnaryOp: rune(tok.text[0]), Inner: &inner}
	case tokOpen:
		inner, err := p.expression(0, depth+1)
		if err != nil {
			return FormulaExpr{}, err
		}
		if !p.takeKind(tokClose) {
			return FormulaExpr{}, FormulaError("公式缺少右括号")
		}
		lhs = inner
	case tokName:
		if !p.takeKind(tokOpen) {
			return FormulaExpr{}, FormulaError("字段引用需要用大括号包裹")
		}
		var args []FormulaExpr
		if !p.takeKind(tokClose) {
			for {
				arg, err := p.expression(0, depth+1)
				if err != nil {
					return FormulaExpr{}, err
				}
				args = append(args, arg)
				if p.takeKind(tokClose) {
					break
				}
				if !p.takeKind(tokComma) {
					return FormulaExpr{}, FormulaError("函数参数需要逗号分隔")
				}
			}
		}
		fn, err := parseFormulaFunc(tok.text, len(args))
		if err != nil {
			return FormulaExpr{}, err
		}
		lhs = FormulaExpr{Kind: ExprCall, Func: fn, Args: args}
	default:
		return FormulaExpr{}, FormulaError("公式缺少表达式")
	}

	for {
		nextTok, ok := p.peek()
		if !ok || nextTok.kind != tokOp {
			break
		}
		var op FormulaOp
		switch nextTok.text {
		case "+":
			op = OpAdd
		case "-":
			op = OpSubtract
		case "*":
			op = OpMultiply
		case "/":
			op = OpDivide
		case "&":
			op = OpConcat
		case "=":
			op = OpEqual
		case "!=":
			op = OpNotEqual
		case "<":
			op = OpLess
		case "<=":
			op = OpLessEqual
		case ">":
			op = OpGreater
		case ">=":
			op = OpGreaterEqual
		default:
			return lhs, nil
		}
		_, prec := op.syntax()
		if prec < minPrec {
			break
		}
		p.pos++
		rhs, err := p.expression(prec+1, depth+1)
		if err != nil {
			return FormulaExpr{}, err
		}
		leftCopy := lhs
		rightCopy := rhs
		lhs = FormulaExpr{Kind: ExprBinary, BinOp: op, Left: &leftCopy, Right: &rightCopy}
	}
	return lhs, nil
}

func (e *FormulaExpr) checkDepth(depth int) error {
	if depth > maxFormulaDepth {
		return FormulaError("公式嵌套过深")
	}
	switch e.Kind {
	case ExprUnary:
		if e.Inner != nil {
			return e.Inner.checkDepth(depth + 1)
		}
	case ExprBinary:
		if e.Left != nil {
			if err := e.Left.checkDepth(depth + 1); err != nil {
				return err
			}
		}
		if e.Right != nil {
			return e.Right.checkDepth(depth + 1)
		}
	case ExprCall:
		for i := range e.Args {
			if err := e.Args[i].checkDepth(depth + 1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Refs collects all referenced field IDs in traversal order.
func (e *FormulaExpr) Refs(out *[]string) {
	switch e.Kind {
	case ExprField:
		*out = append(*out, e.FieldID)
	case ExprUnary:
		if e.Inner != nil {
			e.Inner.Refs(out)
		}
	case ExprBinary:
		if e.Left != nil {
			e.Left.Refs(out)
		}
		if e.Right != nil {
			e.Right.Refs(out)
		}
	case ExprCall:
		for i := range e.Args {
			e.Args[i].Refs(out)
		}
	}
}

// Source renders the AST back to human-readable syntax using current field names.
func (e *FormulaExpr) Source(label func(id string) (string, bool)) string {
	switch e.Kind {
	case ExprLiteral:
		if e.Literal == nil {
			return "BLANK"
		}
		if b, ok := e.Literal.(bool); ok {
			if b {
				return "TRUE"
			}
			return "FALSE"
		}
		raw, err := marshalCompactString(e.Literal)
		if err != nil {
			return fmt.Sprint(e.Literal)
		}
		return raw
	case ExprField:
		if name, ok := label(e.FieldID); ok {
			return "{" + name + "}"
		}
		return "{" + e.FieldID + "}"
	case ExprUnary:
		return fmt.Sprintf("%c(%s)", e.UnaryOp, e.Inner.Source(label))
	case ExprBinary:
		sym, _ := e.BinOp.syntax()
		return fmt.Sprintf("(%s %s %s)", e.Left.Source(label), sym, e.Right.Source(label))
	case ExprCall:
		parts := make([]string, len(e.Args))
		for i := range e.Args {
			parts[i] = e.Args[i].Source(label)
		}
		return fmt.Sprintf("%s(%s)", string(e.Func), strings.Join(parts, ", "))
	default:
		return ""
	}
}

func finiteNumber(n float64) (float64, error) {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, FormulaError("公式结果超出数值范围")
	}
	return n, nil
}

func formulaNumber(v any) (float64, error) {
	if f, ok := asFloat64(v); ok {
		return f, nil
	}
	return 0, FormulaError("公式运算需要数字")
}

func formulaTruthy(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err == nil && f != 0
	case string:
		return len(x) > 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return true
	default:
		return true
	}
}

func flattenFormulaValues(v any, out *[]any) {
	if arr, ok := v.([]any); ok {
		for _, item := range arr {
			flattenFormulaValues(item, out)
		}
		return
	}
	*out = append(*out, v)
}

func formulaText(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return x.String()
	case []any:
		var flat []any
		flattenFormulaValues(x, &flat)
		parts := make([]string, len(flat))
		for i, item := range flat {
			parts[i] = formulaText(item)
		}
		return strings.Join(parts, ", ")
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		raw, err := marshalCompactString(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return raw
	}
}

func formulaTextValue(s string) (any, error) {
	if len(s) > maxFormulaTextBytes {
		return nil, FormulaError("公式文本结果过长")
	}
	return s, nil
}

// Evaluate executes the AST with field lookups delegated to getField.
func (e *FormulaExpr) Evaluate(getField func(id string) (any, error)) (any, error) {
	switch e.Kind {
	case ExprLiteral:
		return cloneJSON(e.Literal), nil
	case ExprField:
		return getField(e.FieldID)
	case ExprUnary:
		val, err := e.Inner.Evaluate(getField)
		if err != nil {
			return nil, err
		}
		switch e.UnaryOp {
		case '!':
			return !formulaTruthy(val), nil
		case '-':
			n, err := formulaNumber(val)
			if err != nil {
				return nil, err
			}
			return finiteNumber(-n)
		default:
			n, err := formulaNumber(val)
			if err != nil {
				return nil, err
			}
			return finiteNumber(n)
		}
	case ExprBinary:
		left, err := e.Left.Evaluate(getField)
		if err != nil {
			return nil, err
		}
		right, err := e.Right.Evaluate(getField)
		if err != nil {
			return nil, err
		}
		cmpVal, comparable := compareFormulaValues(left, right)
		equal := false
		if comparable {
			equal = cmpVal == 0
		} else {
			equal = jsonEqual(left, right)
		}
		switch e.BinOp {
		case OpAdd:
			ln, err := formulaNumber(left)
			if err != nil {
				return nil, err
			}
			rn, err := formulaNumber(right)
			if err != nil {
				return nil, err
			}
			return finiteNumber(ln + rn)
		case OpSubtract:
			ln, err := formulaNumber(left)
			if err != nil {
				return nil, err
			}
			rn, err := formulaNumber(right)
			if err != nil {
				return nil, err
			}
			return finiteNumber(ln - rn)
		case OpMultiply:
			ln, err := formulaNumber(left)
			if err != nil {
				return nil, err
			}
			rn, err := formulaNumber(right)
			if err != nil {
				return nil, err
			}
			return finiteNumber(ln * rn)
		case OpDivide:
			rn, err := formulaNumber(right)
			if err != nil {
				return nil, err
			}
			if rn == 0 {
				return nil, FormulaError("公式不能除以零")
			}
			ln, err := formulaNumber(left)
			if err != nil {
				return nil, err
			}
			return finiteNumber(ln / rn)
		case OpConcat:
			return formulaTextValue(formulaText(left) + formulaText(right))
		case OpEqual:
			return equal, nil
		case OpNotEqual:
			return !equal, nil
		case OpLess:
			return comparable && cmpVal < 0, nil
		case OpLessEqual:
			return comparable && cmpVal <= 0, nil
		case OpGreater:
			return comparable && cmpVal > 0, nil
		case OpGreaterEqual:
			return comparable && cmpVal >= 0, nil
		default:
			return nil, FormulaError("公式运算符无效")
		}
	case ExprCall:
		if e.Func == FuncIf {
			if len(e.Args) != 3 {
				return nil, FormulaError("IF 需要三个参数")
			}
			cond, err := e.Args[0].Evaluate(getField)
			if err != nil {
				return nil, err
			}
			if formulaTruthy(cond) {
				return e.Args[1].Evaluate(getField)
			}
			return e.Args[2].Evaluate(getField)
		}
		args := make([]any, len(e.Args))
		for i := range e.Args {
			val, err := e.Args[i].Evaluate(getField)
			if err != nil {
				return nil, err
			}
			args[i] = val
		}
		return evaluateFormulaCall(e.Func, args)
	default:
		return nil, FormulaError("公式语法树无效")
	}
}

func compareFormulaValues(left, right any) (int, bool) {
	if lf, ok := asFloat64(left); ok {
		if rf, ok := asFloat64(right); ok {
			if math.IsNaN(lf) || math.IsNaN(rf) {
				return 0, false
			}
			if lf < rf {
				return -1, true
			}
			if lf > rf {
				return 1, true
			}
			return 0, true
		}
	}
	if ls, ok := left.(string); ok {
		if rs, ok := right.(string); ok {
			return strings.Compare(ls, rs), true
		}
	}
	return 0, false
}

func evaluateFormulaCall(fn FormulaFunc, args []any) (any, error) {
	if fn == FuncConcatenate {
		var sb strings.Builder
		for _, arg := range args {
			sb.WriteString(formulaText(arg))
		}
		return formulaTextValue(sb.String())
	}
	if fn == FuncArrayJoin {
		if len(args) == 0 {
			return nil, FormulaError("ARRAYJOIN 需要参数")
		}
		sep := ", "
		if len(args) >= 2 {
			sep = formulaText(args[1])
		}
		var flat []any
		flattenFormulaValues(args[0], &flat)
		parts := make([]string, len(flat))
		for i, item := range flat {
			parts[i] = formulaText(item)
		}
		return formulaTextValue(strings.Join(parts, sep))
	}
	var sum float64
	count := 0
	minVal := math.Inf(1)
	maxVal := math.Inf(-1)
	for _, arg := range args {
		var flat []any
		flattenFormulaValues(arg, &flat)
		for _, item := range flat {
			if num, ok := asFloat64(item); ok {
				sum += num
				count++
				if num < minVal {
					minVal = num
				}
				if num > maxVal {
					maxVal = num
				}
			}
		}
	}
	switch fn {
	case FuncSum:
		return finiteNumber(sum)
	case FuncCount:
		return float64(count), nil
	case FuncAverage:
		if count == 0 {
			return nil, nil
		}
		return finiteNumber(sum / float64(count))
	case FuncMin:
		if count == 0 {
			return nil, nil
		}
		return finiteNumber(minVal)
	case FuncMax:
		if count == 0 {
			return nil, nil
		}
		return finiteNumber(maxVal)
	default:
		return nil, FormulaError("公式中有不支持的函数")
	}
}
