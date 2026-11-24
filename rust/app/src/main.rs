mod health;

use std::{env, error::Error, net::Ipv4Addr};

use sqlx::postgres::PgPoolOptions;
use tokio::{net::TcpListener, signal};

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    let port = env::var("PORT")
        .ok()
        .filter(|value| !value.is_empty())
        .map_or(Ok(8080), |value| value.parse::<u16>())?;
    let database_url = env::var("DATABASE_URL")?;
    let database = PgPoolOptions::new()
        .max_connections(2)
        .connect_lazy(&database_url)?;
    let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, port)).await?;
    let app = health::router(database);
    eprintln!(
        "XShadow Rust API candidate listening on {}",
        listener.local_addr()?
    );
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    Ok(())
}

async fn shutdown_signal() {
    #[cfg(unix)]
    if let Ok(mut terminate) = signal::unix::signal(signal::unix::SignalKind::terminate()) {
        tokio::select! {
            _ = signal::ctrl_c() => {},
            _ = terminate.recv() => {},
        }
        return;
    }
    let _ = signal::ctrl_c().await;
}
