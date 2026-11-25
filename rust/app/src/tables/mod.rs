//! Generic sheets: tables, fields, records, cells and views.
//!
//! This aggregate is not a task attribute, a task type, a knowledge source or a
//! cross-sheet link. Those stay separate. A later context may store one of
//! these ids, but it does not add columns or tables here.

mod http;
mod store;
mod validate;

pub use http::router;
