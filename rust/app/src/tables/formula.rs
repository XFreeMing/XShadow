use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

const MAX_SOURCE_BYTES: usize = 4096;
const MAX_NODES: usize = 256;
const MAX_DEPTH: usize = 32;
const MAX_TEXT_BYTES: usize = 32_768;

type FormulaResult<T> = Result<T, &'static str>;

#[derive(Clone, Serialize, Deserialize)]
pub(crate) enum Expr {
    Literal(Value),
    Field(String),
    Unary(char, Box<Expr>),
    Binary(Op, Box<Expr>, Box<Expr>),
    Call(Function, Vec<Expr>),
}

#[derive(Clone, Copy, Serialize, Deserialize)]
pub(crate) enum Op {
    Add,
    Subtract,
    Multiply,
    Divide,
    Concat,
    Equal,
    NotEqual,
    Less,
    LessEqual,
    Greater,
    GreaterEqual,
}

impl Op {
    fn syntax(self) -> (&'static str, u8) {
        match self {
            Self::Equal => ("=", 1),
            Self::NotEqual => ("!=", 1),
            Self::Less => ("<", 1),
            Self::LessEqual => ("<=", 1),
            Self::Greater => (">", 1),
            Self::GreaterEqual => (">=", 1),
            Self::Concat => ("&", 2),
            Self::Add => ("+", 3),
            Self::Subtract => ("-", 3),
            Self::Multiply => ("*", 4),
            Self::Divide => ("/", 4),
        }
    }
}

#[derive(Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "UPPERCASE")]
pub(crate) enum Function {
    If,
    Sum,
    Average,
    Min,
    Max,
    Count,
    Concatenate,
    ArrayJoin,
}

impl Function {
    fn parse(name: &str, args: usize) -> FormulaResult<Self> {
        let function = match name.to_ascii_uppercase().as_str() {
            "IF" => Self::If,
            "SUM" => Self::Sum,
            "AVERAGE" => Self::Average,
            "MIN" => Self::Min,
            "MAX" => Self::Max,
            "COUNT" => Self::Count,
            "CONCATENATE" => Self::Concatenate,
            "ARRAYJOIN" => Self::ArrayJoin,
            _ => return Err("公式中有不支持的函数"),
        };
        match function {
            Self::If if args != 3 => Err("IF 需要三个参数"),
            Self::ArrayJoin if !(1..=2).contains(&args) => Err("ARRAYJOIN 需要一或两个参数"),
            _ => Ok(function),
        }
    }

    fn name(self) -> &'static str {
        match self {
            Self::If => "IF",
            Self::Sum => "SUM",
            Self::Average => "AVERAGE",
            Self::Min => "MIN",
            Self::Max => "MAX",
            Self::Count => "COUNT",
            Self::Concatenate => "CONCATENATE",
            Self::ArrayJoin => "ARRAYJOIN",
        }
    }
}

#[derive(Clone, PartialEq)]
enum Token {
    Literal(Value),
    Field(String),
    Name(String),
    Operator(&'static str),
    Open,
    Close,
    Comma,
}

fn lex(mut source: &str) -> FormulaResult<Vec<Token>> {
    let mut tokens = Vec::new();
    while let Some(c) = source.chars().next() {
        source = &source[c.len_utf8()..];
        let token = match c {
            c if c.is_whitespace() => continue,
            '{' => {
                let (name, tail) = source.split_once('}').ok_or("字段引用缺少右括号")?;
                source = tail;
                Token::Field(name.to_owned())
            }
            quote @ ('"' | '\'') => {
                let mut result = String::new();
                let mut chars = source.char_indices();
                let end = loop {
                    match chars.next() {
                        Some((offset, c)) if c == quote => break offset + c.len_utf8(),
                        Some((_, '\\')) => match chars.next().map(|(_, c)| c) {
                            Some('n') => result.push('\n'),
                            Some('r') => result.push('\r'),
                            Some('t') => result.push('\t'),
                            Some(c @ ('\\' | '\'' | '"')) => result.push(c),
                            _ => return Err("公式文本中的转义无效"),
                        },
                        Some((_, c)) => result.push(c),
                        None => return Err("公式文本缺少结束引号"),
                    }
                };
                source = &source[end..];
                Token::Literal(Value::String(result))
            }
            c if c.is_ascii_digit() || c == '.' => {
                let length = source
                    .bytes()
                    .take_while(|v| v.is_ascii_digit() || *v == b'.')
                    .count();
                let number = format!("{c}{}", &source[..length])
                    .parse::<f64>()
                    .map_err(|_| "公式数字格式无效")?;
                source = &source[length..];
                Token::Literal(finite(number)?)
            }
            c if c.is_ascii_alphabetic() || c == '_' => {
                let length = source
                    .bytes()
                    .take_while(|v| v.is_ascii_alphanumeric() || *v == b'_')
                    .count();
                let name = format!("{c}{}", &source[..length]);
                source = &source[length..];
                match name.to_ascii_uppercase().as_str() {
                    "TRUE" => Token::Literal(Value::Bool(true)),
                    "FALSE" => Token::Literal(Value::Bool(false)),
                    "BLANK" => Token::Literal(Value::Null),
                    _ => Token::Name(name),
                }
            }
            '(' => Token::Open,
            ')' => Token::Close,
            ',' => Token::Comma,
            '+' => Token::Operator("+"),
            '-' => Token::Operator("-"),
            '*' => Token::Operator("*"),
            '/' => Token::Operator("/"),
            '&' => Token::Operator("&"),
            c @ ('=' | '<' | '>' | '!') => {
                let suffix = source.strip_prefix('=');
                let operator = match (c, suffix) {
                    ('=', None) => "=",
                    ('=', Some(_)) => "=",
                    ('<', None) if source.starts_with('>') => {
                        source = &source[1..];
                        "!="
                    }
                    ('<', None) => "<",
                    ('>', None) => ">",
                    ('!', None) => "!",
                    ('<', Some(_)) => "<=",
                    ('>', Some(_)) => ">=",
                    ('!', Some(_)) => "!=",
                    _ => return Err("公式运算符无效"),
                };
                if let Some(tail) = suffix {
                    source = tail;
                }
                Token::Operator(operator)
            }
            _ => return Err("公式中有无法识别的字符"),
        };
        tokens.push(token);
        if tokens.len() > MAX_NODES {
            return Err("公式过于复杂");
        }
    }
    Ok(tokens)
}

pub(crate) fn compile(
    source: &str,
    field_id: impl Fn(&str) -> Option<String>,
) -> FormulaResult<Expr> {
    let source = source.trim();
    let source = source.strip_prefix('=').unwrap_or(source);
    if source.len() > MAX_SOURCE_BYTES {
        return Err("公式过长");
    }
    let tokens = lex(source)?;
    let mut parser = Parser {
        tokens: tokens.into_iter().peekable(),
        field_id,
    };
    let expr = parser.expression(0, 0)?;
    if parser.tokens.next().is_some() {
        return Err("公式表达式未结束");
    }
    expr.check_depth(0)?;
    Ok(expr)
}

struct Parser<F> {
    tokens: std::iter::Peekable<std::vec::IntoIter<Token>>,
    field_id: F,
}

impl<F: Fn(&str) -> Option<String>> Parser<F> {
    fn take(&mut self, token: &Token) -> bool {
        self.tokens.next_if(|next| next == token).is_some()
    }

    fn expression(&mut self, minimum: u8, depth: usize) -> FormulaResult<Expr> {
        if depth > MAX_DEPTH {
            return Err("公式嵌套过深");
        }
        let mut lhs = match self.tokens.next() {
            Some(Token::Literal(value)) => Expr::Literal(value),
            Some(Token::Field(name)) => {
                Expr::Field((self.field_id)(&name).ok_or("公式引用了不存在的字段")?)
            }
            Some(Token::Operator(op @ ("+" | "-" | "!"))) => Expr::Unary(
                op.as_bytes()[0] as char,
                Box::new(self.expression(5, depth + 1)?),
            ),
            Some(Token::Open) => {
                let inner = self.expression(0, depth + 1)?;
                if !self.take(&Token::Close) {
                    return Err("公式缺少右括号");
                }
                inner
            }
            Some(Token::Name(name)) => {
                if !self.take(&Token::Open) {
                    return Err("字段引用需要用大括号包裹");
                }
                let mut args = Vec::new();
                if !self.take(&Token::Close) {
                    loop {
                        args.push(self.expression(0, depth + 1)?);
                        if self.take(&Token::Close) {
                            break;
                        }
                        if !self.take(&Token::Comma) {
                            return Err("函数参数需要逗号分隔");
                        }
                    }
                }
                Expr::Call(Function::parse(&name, args.len())?, args)
            }
            _ => return Err("公式缺少表达式"),
        };
        while let Some(Token::Operator(symbol)) = self.tokens.peek() {
            let op = match *symbol {
                "+" => Op::Add,
                "-" => Op::Subtract,
                "*" => Op::Multiply,
                "/" => Op::Divide,
                "&" => Op::Concat,
                "=" => Op::Equal,
                "!=" => Op::NotEqual,
                "<" => Op::Less,
                "<=" => Op::LessEqual,
                ">" => Op::Greater,
                ">=" => Op::GreaterEqual,
                _ => break,
            };
            let (_, precedence) = op.syntax();
            if precedence < minimum {
                break;
            }
            self.tokens.next();
            let rhs = self.expression(precedence + 1, depth + 1)?;
            lhs = Expr::Binary(op, Box::new(lhs), Box::new(rhs));
        }
        Ok(lhs)
    }
}

fn finite(number: f64) -> FormulaResult<Value> {
    serde_json::Number::from_f64(number)
        .map(Value::Number)
        .ok_or("公式结果超出数值范围")
}

fn number(value: &Value) -> FormulaResult<f64> {
    value.as_f64().ok_or("公式运算需要数字")
}

fn truthy(value: &Value) -> bool {
    match value {
        Value::Null => false,
        Value::Bool(value) => *value,
        Value::Number(value) => value.as_f64().is_some_and(|value| value != 0.0),
        Value::String(value) => !value.is_empty(),
        Value::Array(value) => !value.is_empty(),
        Value::Object(_) => true,
    }
}

fn list_values(value: &Value) -> Box<dyn Iterator<Item = &Value> + '_> {
    match value {
        Value::Array(values) => Box::new(values.iter().flat_map(list_values)),
        other => Box::new(std::iter::once(other)),
    }
}

fn text(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(value) => value.clone(),
        Value::Number(value) => value
            .as_f64()
            .map_or_else(|| value.to_string(), |v| v.to_string()),
        Value::Array(_) => list_values(value).map(text).collect::<Vec<_>>().join(", "),
        other => other.to_string(),
    }
}

fn text_value(value: String) -> FormulaResult<Value> {
    if value.len() > MAX_TEXT_BYTES {
        return Err("公式文本结果过长");
    }
    Ok(Value::String(value))
}

impl Expr {
    fn check_depth(&self, depth: usize) -> FormulaResult<()> {
        if depth > MAX_DEPTH {
            return Err("公式嵌套过深");
        }
        match self {
            Self::Unary(_, inner) => inner.check_depth(depth + 1),
            Self::Binary(_, left, right) => {
                left.check_depth(depth + 1)?;
                right.check_depth(depth + 1)
            }
            Self::Call(_, args) => args.iter().try_for_each(|arg| arg.check_depth(depth + 1)),
            _ => Ok(()),
        }
    }

    pub(crate) fn refs(&self, out: &mut Vec<String>) {
        match self {
            Self::Field(id) => out.push(id.clone()),
            Self::Unary(_, inner) => inner.refs(out),
            Self::Binary(_, left, right) => {
                left.refs(out);
                right.refs(out);
            }
            Self::Call(_, args) => args.iter().for_each(|arg| arg.refs(out)),
            Self::Literal(_) => {}
        }
    }

    pub(crate) fn source(&self, label: &impl Fn(&str) -> Option<String>) -> String {
        match self {
            Self::Literal(Value::Null) => "BLANK".to_owned(),
            Self::Literal(Value::Bool(value)) => value.to_string().to_ascii_uppercase(),
            Self::Literal(value) => value.to_string(),
            Self::Field(id) => format!("{{{}}}", label(id).as_deref().unwrap_or(id)),
            Self::Unary(op, inner) => format!("{op}({})", inner.source(label)),
            Self::Binary(op, left, right) => format!(
                "({} {} {})",
                left.source(label),
                op.syntax().0,
                right.source(label)
            ),
            Self::Call(function, args) => {
                let args = args
                    .iter()
                    .map(|arg| arg.source(label))
                    .collect::<Vec<_>>()
                    .join(", ");
                format!("{}({args})", function.name())
            }
        }
    }

    pub(crate) fn evaluate(
        &self,
        field: &mut impl FnMut(&str) -> FormulaResult<Value>,
    ) -> FormulaResult<Value> {
        match self {
            Self::Literal(value) => Ok(value.clone()),
            Self::Field(id) => field(id),
            Self::Unary(op, arg) => {
                let value = arg.evaluate(field)?;
                match op {
                    '!' => Ok(json!(!truthy(&value))),
                    '-' => finite(-number(&value)?),
                    _ => finite(number(&value)?),
                }
            }
            Self::Binary(op, left, right) => {
                let left = left.evaluate(field)?;
                let right = right.evaluate(field)?;
                let compare = match (left.as_f64(), right.as_f64()) {
                    (Some(left), Some(right)) => left.partial_cmp(&right),
                    _ => match (left.as_str(), right.as_str()) {
                        (Some(left), Some(right)) => Some(left.cmp(right)),
                        _ => None,
                    },
                };
                let equal = compare.map_or(left == right, std::cmp::Ordering::is_eq);
                match op {
                    Op::Add => finite(number(&left)? + number(&right)?),
                    Op::Subtract => finite(number(&left)? - number(&right)?),
                    Op::Multiply => finite(number(&left)? * number(&right)?),
                    Op::Divide => {
                        let divisor = number(&right)?;
                        if divisor == 0.0 {
                            return Err("公式不能除以零");
                        }
                        finite(number(&left)? / divisor)
                    }
                    Op::Concat => text_value(text(&left) + &text(&right)),
                    Op::Equal => Ok(json!(equal)),
                    Op::NotEqual => Ok(json!(!equal)),
                    Op::Less => Ok(json!(compare.is_some_and(|value| value.is_lt()))),
                    Op::LessEqual => Ok(json!(compare.is_some_and(|value| value.is_le()))),
                    Op::Greater => Ok(json!(compare.is_some_and(|value| value.is_gt()))),
                    Op::GreaterEqual => Ok(json!(compare.is_some_and(|value| value.is_ge()))),
                }
            }
            Self::Call(Function::If, args) => {
                let [condition, yes, no] = args.as_slice() else {
                    return Err("IF 需要三个参数");
                };
                if truthy(&condition.evaluate(field)?) {
                    yes.evaluate(field)
                } else {
                    no.evaluate(field)
                }
            }
            Self::Call(function, args) => {
                let args = args
                    .iter()
                    .map(|arg| arg.evaluate(field))
                    .collect::<FormulaResult<Vec<_>>>()?;
                evaluate_call(*function, &args)
            }
        }
    }
}

fn evaluate_call(function: Function, args: &[Value]) -> FormulaResult<Value> {
    if matches!(function, Function::Concatenate) {
        return text_value(args.iter().map(text).collect::<String>());
    }
    if matches!(function, Function::ArrayJoin) {
        let [value, rest @ ..] = args else {
            return Err("ARRAYJOIN 需要参数");
        };
        let separator = rest.first().map_or_else(|| ", ".to_owned(), text);
        return text_value(
            list_values(value)
                .map(text)
                .collect::<Vec<_>>()
                .join(&separator),
        );
    }
    let values = args.iter().flat_map(list_values).filter_map(Value::as_f64);
    let (sum, count, min, max) = values.fold(
        (0.0, 0, f64::INFINITY, f64::NEG_INFINITY),
        |(sum, count, min, max), value| (sum + value, count + 1, min.min(value), max.max(value)),
    );
    match function {
        Function::Sum => finite(sum),
        Function::Count => Ok(json!(count)),
        Function::Average | Function::Min | Function::Max if count == 0 => Ok(Value::Null),
        Function::Average => finite(sum / f64::from(count)),
        Function::Min => finite(min),
        Function::Max => finite(max),
        _ => Err("公式中有不支持的函数"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    #[test]
    fn parser_respects_precedence_unary_and_leading_equals() {
        let expr = compile("=1 + 2 * 3", |_| None).unwrap();
        assert_eq!(expr.evaluate(&mut |_| Ok(Value::Null)).unwrap(), json!(7.0));

        let grouped = compile("(1 + 2) * -3", |_| None).unwrap();
        assert_eq!(
            grouped.evaluate(&mut |_| Ok(Value::Null)).unwrap(),
            json!(-9.0)
        );

        let negated = compile("!FALSE", |_| None).unwrap();
        assert_eq!(
            negated.evaluate(&mut |_| Ok(Value::Null)).unwrap(),
            json!(true)
        );
    }

    #[test]
    fn parser_compiles_field_refs_to_ids_and_renders_renamed_labels() {
        let expr = compile("{单价} * {数量}", |name| match name {
            "单价" => Some("f-price".to_owned()),
            "数量" => Some("f-qty".to_owned()),
            _ => None,
        })
        .unwrap();

        let mut refs = Vec::new();
        expr.refs(&mut refs);
        assert_eq!(refs, vec!["f-price".to_owned(), "f-qty".to_owned()]);

        let rendered = expr.source(&|id| match id {
            "f-price" => Some("最新单价".to_owned()),
            "f-qty" => Some("件数".to_owned()),
            _ => None,
        });
        assert_eq!(rendered, "({最新单价} * {件数})");

        let mut row = BTreeMap::new();
        row.insert("f-price", json!(12.5));
        row.insert("f-qty", json!(4));
        assert_eq!(
            expr.evaluate(&mut |id| Ok(row.get(id).cloned().unwrap_or(Value::Null)))
                .unwrap(),
            json!(50.0)
        );
    }

    #[test]
    fn functions_and_comparisons_evaluate_scalars_and_lookup_arrays() {
        let expr = compile(
            "IF(SUM({nums}) >= 10, ARRAYJOIN({tags}, ' / '), CONCATENATE('low:', COUNT({nums})))",
            |name| match name {
                "nums" => Some("f-nums".to_owned()),
                "tags" => Some("f-tags".to_owned()),
                _ => None,
            },
        )
        .unwrap();

        let high = expr
            .evaluate(&mut |id| match id {
                "f-nums" => Ok(json!([0, 4, null, 6])),
                "f-tags" => Ok(json!(["A", "B"])),
                _ => Ok(Value::Null),
            })
            .unwrap();
        assert_eq!(high, json!("A / B"));

        let low = expr
            .evaluate(&mut |id| match id {
                "f-nums" => Ok(json!([0, false, null, 3])),
                "f-tags" => Ok(json!([])),
                _ => Ok(Value::Null),
            })
            .unwrap();
        assert_eq!(low, json!("low:2"));

        let stats = compile("AVERAGE({nums}) + MIN({nums}) + MAX({nums})", |name| {
            (name == "nums").then(|| "f-nums".to_owned())
        })
        .unwrap();
        assert_eq!(
            stats.evaluate(&mut |_| Ok(json!([2, 4, 6]))).unwrap(),
            json!(12.0)
        );

        let empty_avg = compile("AVERAGE({nums})", |_| Some("f-nums".to_owned())).unwrap();
        assert_eq!(
            empty_avg.evaluate(&mut |_| Ok(json!([]))).unwrap(),
            Value::Null
        );
    }

    #[test]
    fn parser_and_evaluator_reject_invalid_syntax_unknown_refs_and_div_zero() {
        assert!(compile("", |_| None).is_err());
        assert!(compile("1 +", |_| None).is_err());
        assert!(compile("(1 + 2", |_| None).is_err());
        assert!(compile("1 2", |_| None).is_err());
        assert!(compile("{missing}", |_| None).is_err());
        assert!(compile("UNKNOWN(1)", |_| None).is_err());
        assert!(compile("IF(TRUE, 1)", |_| None).is_err());
        assert!(compile("ARRAYJOIN({a}, ',', 'extra')", |_| Some("a".to_owned())).is_err());
        assert!(compile("'unterminated", |_| None).is_err());
        assert!(compile("{unclosed", |_| None).is_err());

        let div_zero = compile("1 / 0", |_| None).unwrap();
        assert_eq!(
            div_zero.evaluate(&mut |_| Ok(Value::Null)).unwrap_err(),
            "公式不能除以零"
        );
    }
}
