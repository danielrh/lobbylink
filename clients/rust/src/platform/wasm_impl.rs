use wasm_bindgen::prelude::*;

pub fn warn(msg: &str) {
    web_sys::console::warn_1(&JsValue::from_str(&format!("[lobbylink] {msg}")));
}

/// Milliseconds; Date.now() is fine for our timeout bookkeeping.
pub fn now_ms() -> f64 {
    js_sys::Date::now()
}

/// setTimeout-backed sleep (no gloo, no tokio).
pub async fn sleep_ms(ms: u64) {
    let promise = js_sys::Promise::new(&mut |resolve, _reject| {
        let ok = web_sys::window()
            .expect("no window: the wasm backend only runs in a browser")
            .set_timeout_with_callback_and_timeout_and_arguments_0(
                &resolve,
                ms.min(i32::MAX as u64) as i32,
            );
        if ok.is_err() {
            // Can't schedule: resolve immediately rather than hang.
            let _ = resolve.call0(&JsValue::NULL);
        }
    });
    let _ = wasm_bindgen_futures::JsFuture::from(promise).await;
}
