//! Tiny async utilities shared by both backends, built on
//! futures-channel/futures-core only (futures-util is native-only).

// race/timeout_ms exist for the wasm backend (which has no tokio
// timers); the native backend uses tokio::time directly.
#![cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]

use std::future::{poll_fn, Future};
use std::pin::pin;
use std::task::Poll;

use futures_channel::mpsc::UnboundedReceiver;
use futures_core::Stream;

/// Await the next item from an UnboundedReceiver without futures-util.
pub async fn recv<T>(rx: &mut UnboundedReceiver<T>) -> Option<T> {
    poll_fn(|cx| std::pin::Pin::new(&mut *rx).poll_next(cx)).await
}

pub enum Either<A, B> {
    Left(A),
    Right(B),
}

/// Race two futures; the loser is dropped.
pub async fn race<A: Future, B: Future>(a: A, b: B) -> Either<A::Output, B::Output> {
    let mut a = pin!(a);
    let mut b = pin!(b);
    poll_fn(|cx| {
        if let Poll::Ready(v) = a.as_mut().poll(cx) {
            return Poll::Ready(Either::Left(v));
        }
        if let Poll::Ready(v) = b.as_mut().poll(cx) {
            return Poll::Ready(Either::Right(v));
        }
        Poll::Pending
    })
    .await
}

/// `Some(output)` if `f` finishes within `ms`, else `None`.
pub async fn timeout_ms<F: Future>(ms: u64, f: F) -> Option<F::Output> {
    match race(f, crate::platform::sleep_ms(ms)).await {
        Either::Left(v) => Some(v),
        Either::Right(()) => None,
    }
}
