//! Generic sheets: tables, fields, records, cells and views.
//!
//! This aggregate is not a task attribute, a task type, a knowledge source or a
//! cross-sheet link. Those stay separate. A later context may store one of
//! these ids, but it does not add columns or tables here.

mod batch;
mod collab;
mod comments;
pub(crate) mod formula;
mod history;
mod http;
pub(crate) mod issue_derived;
mod projection;
mod relation;
pub(crate) mod store;
mod validate;

pub use http::router;
pub(crate) use formula::Expr as FormulaExpr;
