package kvm

import "github.com/pion/logging"

// we use logging framework from pion
// ref: https://github.com/pion/webrtc/wiki/Debugging-WebRTC
var defaultLoggerFactory = logging.NewDefaultLoggerFactory()
var logger = defaultLoggerFactory.NewLogger("jetkvm")
var cloudLogger = defaultLoggerFactory.NewLogger("cloud")
var websocketLogger = defaultLoggerFactory.NewLogger("websocket")
