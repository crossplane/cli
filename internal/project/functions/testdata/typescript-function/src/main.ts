import leftPad from "left-pad";

// A real function serves gRPC and keeps running; this fixture only needs to
// prove that a build reaches an image whose entrypoint starts, resolves its
// npm dependency, and exits cleanly.
console.log(leftPad("hi", 5, "*"));
