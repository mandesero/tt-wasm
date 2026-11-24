#[cfg_attr(not(target_arch = "wasm32"), path = "../.tt-wasm/bindings/bindings.rs")]
mod bindings;

struct Component;

impl bindings::Guest for Component {
    fn add(a: i32, b: i32) -> i32 {
        a + b
    }
}

bindings::export!(Component with_types_in bindings);
