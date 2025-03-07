package kvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/pion/webrtc/v4"
	"go.bug.st/serial"
)

type JSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      interface{}            `json:"id,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

type JSONRPCEvent struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type BacklightSettings struct {
	MaxBrightness int `json:"max_brightness"`
	DimAfter      int `json:"dim_after"`
	OffAfter      int `json:"off_after"`
}

func writeJSONRPCResponse(response JSONRPCResponse, session *Session) {
	responseBytes, err := json.Marshal(response)
	if err != nil {
		log.Println("Error marshalling JSONRPC response:", err)
		return
	}
	err = session.RPCChannel.SendText(string(responseBytes))
	if err != nil {
		log.Println("Error sending JSONRPC response:", err)
		return
	}
}

func writeJSONRPCEvent(event string, params interface{}, session *Session) {
	request := JSONRPCEvent{
		JSONRPC: "2.0",
		Method:  event,
		Params:  params,
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		log.Println("Error marshalling JSONRPC event:", err)
		return
	}
	if session == nil || session.RPCChannel == nil {
		log.Println("RPC channel not available")
		return
	}
	err = session.RPCChannel.SendText(string(requestBytes))
	if err != nil {
		log.Println("Error sending JSONRPC event:", err)
		return
	}
}

func onRPCMessage(message webrtc.DataChannelMessage, session *Session) {
	var request JSONRPCRequest
	err := json.Unmarshal(message.Data, &request)
	if err != nil {
		errorResponse := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32700,
				"message": "Parse error",
			},
			ID: 0,
		}
		writeJSONRPCResponse(errorResponse, session)
		return
	}

	//log.Printf("Received RPC request: Method=%s, Params=%v, ID=%d", request.Method, request.Params, request.ID)
	handler, ok := rpcHandlers[request.Method]
	if !ok {
		errorResponse := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
			},
			ID: request.ID,
		}
		writeJSONRPCResponse(errorResponse, session)
		return
	}

	result, err := callRPCHandler(handler, request.Params)
	if err != nil {
		errorResponse := JSONRPCResponse{
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32603,
				"message": "Internal error",
				"data":    err.Error(),
			},
			ID: request.ID,
		}
		writeJSONRPCResponse(errorResponse, session)
		return
	}

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      request.ID,
	}
	writeJSONRPCResponse(response, session)
}

func rpcPing() (string, error) {
	return "pong", nil
}

func rpcGetDeviceID() (string, error) {
	return GetDeviceID(), nil
}

var streamFactor = 1.0

func rpcGetStreamQualityFactor() (float64, error) {
	return streamFactor, nil
}

func rpcSetStreamQualityFactor(factor float64) error {
	log.Printf("Setting stream quality factor to: %f", factor)
	var _, err = CallCtrlAction("set_video_quality_factor", map[string]interface{}{"quality_factor": factor})
	if err != nil {
		return err
	}

	streamFactor = factor
	return nil
}

func rpcGetAutoUpdateState() (bool, error) {
	return config.AutoUpdateEnabled, nil
}

func rpcSetAutoUpdateState(enabled bool) (bool, error) {
	config.AutoUpdateEnabled = enabled
	if err := SaveConfig(); err != nil {
		return config.AutoUpdateEnabled, fmt.Errorf("failed to save config: %w", err)
	}
	return enabled, nil
}

func rpcGetEDID() (string, error) {
	resp, err := CallCtrlAction("get_edid", nil)
	if err != nil {
		return "", err
	}
	edid, ok := resp.Result["edid"]
	if ok {
		return edid.(string), nil
	}
	return "", errors.New("EDID not found in response")
}

func rpcSetEDID(edid string) error {
	if edid == "" {
		log.Println("Restoring EDID to default")
		edid = "00ffffffffffff0052620188008888881c150103800000780a0dc9a05747982712484c00000001010101010101010101010101010101023a801871382d40582c4500c48e2100001e011d007251d01e206e285500c48e2100001e000000fc00543734392d6648443732300a20000000fd00147801ff1d000a202020202020017b"
	} else {
		log.Printf("Setting EDID to: %s", edid)
	}
	_, err := CallCtrlAction("set_edid", map[string]interface{}{"edid": edid})
	if err != nil {
		return err
	}

	// Save EDID to config, allowing it to be restored on reboot.
	config.EdidString = edid
	SaveConfig()

	return nil
}

func rpcGetDevChannelState() (bool, error) {
	return config.IncludePreRelease, nil
}

func rpcSetDevChannelState(enabled bool) error {
	config.IncludePreRelease = enabled
	if err := SaveConfig(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

func rpcGetUpdateStatus() (*UpdateStatus, error) {
	includePreRelease := config.IncludePreRelease
	updateStatus, err := GetUpdateStatus(context.Background(), GetDeviceID(), includePreRelease)
	if err != nil {
		return nil, fmt.Errorf("error checking for updates: %w", err)
	}

	return updateStatus, nil
}

func rpcTryUpdate() error {
	includePreRelease := config.IncludePreRelease
	go func() {
		err := TryUpdate(context.Background(), GetDeviceID(), includePreRelease)
		if err != nil {
			logger.Warnf("failed to try update: %v", err)
		}
	}()
	return nil
}

func rpcSetBacklightSettings(params BacklightSettings) error {
	blConfig := params

	// NOTE: by default, the frontend limits the brightness to 64, as that's what the device originally shipped with.
	if blConfig.MaxBrightness > 255 || blConfig.MaxBrightness < 0 {
		return fmt.Errorf("maxBrightness must be between 0 and 255")
	}

	if blConfig.DimAfter < 0 {
		return fmt.Errorf("dimAfter must be a positive integer")
	}

	if blConfig.OffAfter < 0 {
		return fmt.Errorf("offAfter must be a positive integer")
	}

	config.DisplayMaxBrightness = blConfig.MaxBrightness
	config.DisplayDimAfterSec = blConfig.DimAfter
	config.DisplayOffAfterSec = blConfig.OffAfter

	if err := SaveConfig(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	log.Printf("rpc: display: settings applied, max_brightness: %d, dim after: %ds, off after: %ds", config.DisplayMaxBrightness, config.DisplayDimAfterSec, config.DisplayOffAfterSec)

	// If the device started up with auto-dim and/or auto-off set to zero, the display init
	// method will not have started the tickers. So in case that has changed, attempt to start the tickers now.
	startBacklightTickers()

	// Wake the display after the settings are altered, this ensures the tickers
	// are reset to the new settings, and will bring the display up to maxBrightness.
	// Calling with force set to true, to ignore the current state of the display, and force
	// it to reset the tickers.
	wakeDisplay(true)
	return nil
}

func rpcGetBacklightSettings() (*BacklightSettings, error) {
	return &BacklightSettings{
		MaxBrightness: config.DisplayMaxBrightness,
		DimAfter:      int(config.DisplayDimAfterSec),
		OffAfter:      int(config.DisplayOffAfterSec),
	}, nil
}

const (
	devModeFile = "/userdata/jetkvm/devmode.enable"
	sshKeyDir   = "/userdata/dropbear/.ssh"
	sshKeyFile  = "/userdata/dropbear/.ssh/authorized_keys"
)

type DevModeState struct {
	Enabled bool `json:"enabled"`
}

type SSHKeyState struct {
	SSHKey string `json:"sshKey"`
}

func rpcGetDevModeState() (DevModeState, error) {
	devModeEnabled := false
	if _, err := os.Stat(devModeFile); err != nil {
		if !os.IsNotExist(err) {
			return DevModeState{}, fmt.Errorf("error checking dev mode file: %w", err)
		}
	} else {
		devModeEnabled = true
	}

	return DevModeState{
		Enabled: devModeEnabled,
	}, nil
}

func rpcSetDevModeState(enabled bool) error {
	if enabled {
		if _, err := os.Stat(devModeFile); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(devModeFile), 0755); err != nil {
				return fmt.Errorf("failed to create directory for devmode file: %w", err)
			}
			if err := os.WriteFile(devModeFile, []byte{}, 0644); err != nil {
				return fmt.Errorf("failed to create devmode file: %w", err)
			}
		} else {
			logger.Debug("dev mode already enabled")
			return nil
		}
	} else {
		if _, err := os.Stat(devModeFile); err == nil {
			if err := os.Remove(devModeFile); err != nil {
				return fmt.Errorf("failed to remove devmode file: %w", err)
			}
		} else if os.IsNotExist(err) {
			logger.Debug("dev mode already disabled")
			return nil
		} else {
			return fmt.Errorf("error checking dev mode file: %w", err)
		}
	}

	cmd := exec.Command("dropbear.sh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Warnf("Failed to start/stop SSH: %v, %v", err, output)
		return fmt.Errorf("failed to start/stop SSH, you may need to reboot for changes to take effect")
	}

	return nil
}

func rpcGetSSHKeyState() (string, error) {
	keyData, err := os.ReadFile(sshKeyFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("error reading SSH key file: %w", err)
		}
	}
	return string(keyData), nil
}

func rpcSetSSHKeyState(sshKey string) error {
	if sshKey != "" {
		// Create directory if it doesn't exist
		if err := os.MkdirAll(sshKeyDir, 0700); err != nil {
			return fmt.Errorf("failed to create SSH key directory: %w", err)
		}

		// Write SSH key to file
		if err := os.WriteFile(sshKeyFile, []byte(sshKey), 0600); err != nil {
			return fmt.Errorf("failed to write SSH key: %w", err)
		}
	} else {
		// Remove SSH key file if empty string is provided
		if err := os.Remove(sshKeyFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove SSH key file: %w", err)
		}
	}

	return nil
}

func callRPCHandler(handler RPCHandler, params map[string]interface{}) (interface{}, error) {
	handlerValue := reflect.ValueOf(handler.Func)
	handlerType := handlerValue.Type()

	if handlerType.Kind() != reflect.Func {
		return nil, errors.New("handler is not a function")
	}

	numParams := handlerType.NumIn()
	args := make([]reflect.Value, numParams)
	// Get the parameter names from the RPCHandler
	paramNames := handler.Params

	if len(paramNames) != numParams {
		return nil, errors.New("mismatch between handler parameters and defined parameter names")
	}

	for i := 0; i < numParams; i++ {
		paramType := handlerType.In(i)
		paramName := paramNames[i]
		paramValue, ok := params[paramName]
		if !ok {
			return nil, errors.New("missing parameter: " + paramName)
		}

		convertedValue := reflect.ValueOf(paramValue)
		if !convertedValue.Type().ConvertibleTo(paramType) {
			if paramType.Kind() == reflect.Slice && (convertedValue.Kind() == reflect.Slice || convertedValue.Kind() == reflect.Array) {
				newSlice := reflect.MakeSlice(paramType, convertedValue.Len(), convertedValue.Len())
				for j := 0; j < convertedValue.Len(); j++ {
					elemValue := convertedValue.Index(j)
					if elemValue.Kind() == reflect.Interface {
						elemValue = elemValue.Elem()
					}
					if !elemValue.Type().ConvertibleTo(paramType.Elem()) {
						// Handle float64 to uint8 conversion
						if elemValue.Kind() == reflect.Float64 && paramType.Elem().Kind() == reflect.Uint8 {
							intValue := int(elemValue.Float())
							if intValue < 0 || intValue > 255 {
								return nil, fmt.Errorf("value out of range for uint8: %v", intValue)
							}
							newSlice.Index(j).SetUint(uint64(intValue))
						} else {
							fromType := elemValue.Type()
							toType := paramType.Elem()
							return nil, fmt.Errorf("invalid element type in slice for parameter %s: from %v to %v", paramName, fromType, toType)
						}
					} else {
						newSlice.Index(j).Set(elemValue.Convert(paramType.Elem()))
					}
				}
				args[i] = newSlice
			} else if paramType.Kind() == reflect.Struct && convertedValue.Kind() == reflect.Map {
				jsonData, err := json.Marshal(convertedValue.Interface())
				if err != nil {
					return nil, fmt.Errorf("failed to marshal map to JSON: %v", err)
				}

				newStruct := reflect.New(paramType).Interface()
				if err := json.Unmarshal(jsonData, newStruct); err != nil {
					return nil, fmt.Errorf("failed to unmarshal JSON into struct: %v", err)
				}
				args[i] = reflect.ValueOf(newStruct).Elem()
			} else {
				return nil, fmt.Errorf("invalid parameter type for: %s, type: %s", paramName, paramType.Kind())
			}
		} else {
			args[i] = convertedValue.Convert(paramType)
		}
	}

	results := handlerValue.Call(args)

	if len(results) == 0 {
		return nil, nil
	}

	if len(results) == 1 {
		if results[0].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			if !results[0].IsNil() {
				return nil, results[0].Interface().(error)
			}
			return nil, nil
		}
		return results[0].Interface(), nil
	}

	if len(results) == 2 && results[1].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
		if !results[1].IsNil() {
			return nil, results[1].Interface().(error)
		}
		return results[0].Interface(), nil
	}

	return nil, errors.New("unexpected return values from handler")
}

type RPCHandler struct {
	Func   interface{}
	Params []string
}

func rpcSetMassStorageMode(mode string) (string, error) {
	log.Printf("[jsonrpc.go:rpcSetMassStorageMode] Setting mass storage mode to: %s", mode)
	var cdrom bool
	if mode == "cdrom" {
		cdrom = true
	} else if mode != "file" {
		log.Printf("[jsonrpc.go:rpcSetMassStorageMode] Invalid mode provided: %s", mode)
		return "", fmt.Errorf("invalid mode: %s", mode)
	}

	log.Printf("[jsonrpc.go:rpcSetMassStorageMode] Setting mass storage mode to: %s", mode)

	err := setMassStorageMode(cdrom)
	if err != nil {
		return "", fmt.Errorf("failed to set mass storage mode: %w", err)
	}

	log.Printf("[jsonrpc.go:rpcSetMassStorageMode] Mass storage mode set to %s", mode)

	// Get the updated mode after setting
	return rpcGetMassStorageMode()
}

func rpcGetMassStorageMode() (string, error) {
	cdrom, err := getMassStorageMode()
	if err != nil {
		return "", fmt.Errorf("failed to get mass storage mode: %w", err)
	}

	mode := "file"
	if cdrom {
		mode = "cdrom"
	}
	return mode, nil
}

func rpcIsUpdatePending() (bool, error) {
	return IsUpdatePending(), nil
}

var udcFilePath = filepath.Join("/sys/bus/platform/drivers/dwc3", udc)

func rpcGetUsbEmulationState() (bool, error) {
	_, err := os.Stat(udcFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error checking USB emulation state: %w", err)
	}
	return true, nil
}

func rpcSetUsbEmulationState(enabled bool) error {
	if enabled {
		return os.WriteFile("/sys/bus/platform/drivers/dwc3/bind", []byte(udc), 0644)
	} else {
		return os.WriteFile("/sys/bus/platform/drivers/dwc3/unbind", []byte(udc), 0644)
	}
}

func rpcGetWakeOnLanDevices() ([]WakeOnLanDevice, error) {
	if config.WakeOnLanDevices == nil {
		return []WakeOnLanDevice{}, nil
	}
	return config.WakeOnLanDevices, nil
}

type SetWakeOnLanDevicesParams struct {
	Devices []WakeOnLanDevice `json:"devices"`
}

func rpcSetWakeOnLanDevices(params SetWakeOnLanDevicesParams) error {
	config.WakeOnLanDevices = params.Devices
	return SaveConfig()
}

func rpcResetConfig() error {
	config = defaultConfig
	if err := SaveConfig(); err != nil {
		return fmt.Errorf("failed to reset config: %w", err)
	}

	log.Println("Configuration reset to default")
	return nil
}

type DCPowerState struct {
	IsOn    bool    `json:"isOn"`
	Voltage float64 `json:"voltage"`
	Current float64 `json:"current"`
	Power   float64 `json:"power"`
}

func rpcGetDCPowerState() (DCPowerState, error) {
	return dcState, nil
}

func rpcSetDCPowerState(enabled bool) error {
	log.Printf("[jsonrpc.go:rpcSetDCPowerState] Setting DC power state to: %v", enabled)
	err := setDCPowerState(enabled)
	if err != nil {
		return fmt.Errorf("failed to set DC power state: %w", err)
	}
	return nil
}

func rpcGetActiveExtension() (string, error) {
	return config.ActiveExtension, nil
}

func rpcSetActiveExtension(extensionId string) error {
	if config.ActiveExtension == extensionId {
		return nil
	}
	if config.ActiveExtension == "atx-power" {
		unmountATXControl()
	} else if config.ActiveExtension == "dc-power" {
		unmountDCControl()
	}
	config.ActiveExtension = extensionId
	if err := SaveConfig(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	if extensionId == "atx-power" {
		mountATXControl()
	} else if extensionId == "dc-power" {
		mountDCControl()
	}
	return nil
}

func rpcSetATXPowerAction(action string) error {
	logger.Debugf("[jsonrpc.go:rpcSetATXPowerAction] Executing ATX power action: %s", action)
	switch action {
	case "power-short":
		logger.Debug("[jsonrpc.go:rpcSetATXPowerAction] Simulating short power button press")
		return pressATXPowerButton(200 * time.Millisecond)
	case "power-long":
		logger.Debug("[jsonrpc.go:rpcSetATXPowerAction] Simulating long power button press")
		return pressATXPowerButton(5 * time.Second)
	case "reset":
		logger.Debug("[jsonrpc.go:rpcSetATXPowerAction] Simulating reset button press")
		return pressATXResetButton(200 * time.Millisecond)
	default:
		return fmt.Errorf("invalid action: %s", action)
	}
}

type ATXState struct {
	Power bool `json:"power"`
	HDD   bool `json:"hdd"`
}

func rpcGetATXState() (ATXState, error) {
	state := ATXState{
		Power: ledPWRState,
		HDD:   ledHDDState,
	}
	return state, nil
}

type SerialSettings struct {
	BaudRate string `json:"baudRate"`
	DataBits string `json:"dataBits"`
	StopBits string `json:"stopBits"`
	Parity   string `json:"parity"`
}

func rpcGetSerialSettings() (SerialSettings, error) {
	settings := SerialSettings{
		BaudRate: strconv.Itoa(serialPortMode.BaudRate),
		DataBits: strconv.Itoa(serialPortMode.DataBits),
		StopBits: "1",
		Parity:   "none",
	}

	switch serialPortMode.StopBits {
	case serial.OneStopBit:
		settings.StopBits = "1"
	case serial.OnePointFiveStopBits:
		settings.StopBits = "1.5"
	case serial.TwoStopBits:
		settings.StopBits = "2"
	}

	switch serialPortMode.Parity {
	case serial.NoParity:
		settings.Parity = "none"
	case serial.OddParity:
		settings.Parity = "odd"
	case serial.EvenParity:
		settings.Parity = "even"
	case serial.MarkParity:
		settings.Parity = "mark"
	case serial.SpaceParity:
		settings.Parity = "space"
	}

	return settings, nil
}

var serialPortMode = defaultMode

func rpcSetSerialSettings(settings SerialSettings) error {
	baudRate, err := strconv.Atoi(settings.BaudRate)
	if err != nil {
		return fmt.Errorf("invalid baud rate: %v", err)
	}
	dataBits, err := strconv.Atoi(settings.DataBits)
	if err != nil {
		return fmt.Errorf("invalid data bits: %v", err)
	}

	var stopBits serial.StopBits
	switch settings.StopBits {
	case "1":
		stopBits = serial.OneStopBit
	case "1.5":
		stopBits = serial.OnePointFiveStopBits
	case "2":
		stopBits = serial.TwoStopBits
	default:
		return fmt.Errorf("invalid stop bits: %s", settings.StopBits)
	}

	var parity serial.Parity
	switch settings.Parity {
	case "none":
		parity = serial.NoParity
	case "odd":
		parity = serial.OddParity
	case "even":
		parity = serial.EvenParity
	case "mark":
		parity = serial.MarkParity
	case "space":
		parity = serial.SpaceParity
	default:
		return fmt.Errorf("invalid parity: %s", settings.Parity)
	}
	serialPortMode = &serial.Mode{
		BaudRate: baudRate,
		DataBits: dataBits,
		StopBits: stopBits,
		Parity:   parity,
	}

	port.SetMode(serialPortMode)

	return nil
}

var rpcHandlers = map[string]RPCHandler{
	"ping":                   {Func: rpcPing},
	"getDeviceID":            {Func: rpcGetDeviceID},
	"deregisterDevice":       {Func: rpcDeregisterDevice},
	"getCloudState":          {Func: rpcGetCloudState},
	"keyboardReport":         {Func: rpcKeyboardReport, Params: []string{"modifier", "keys"}},
	"absMouseReport":         {Func: rpcAbsMouseReport, Params: []string{"x", "y", "buttons"}},
	"relMouseReport":         {Func: rpcRelMouseReport, Params: []string{"dx", "dy", "buttons"}},
	"wheelReport":            {Func: rpcWheelReport, Params: []string{"wheelY"}},
	"getVideoState":          {Func: rpcGetVideoState},
	"getUSBState":            {Func: rpcGetUSBState},
	"unmountImage":           {Func: rpcUnmountImage},
	"rpcMountBuiltInImage":   {Func: rpcMountBuiltInImage, Params: []string{"filename"}},
	"setJigglerState":        {Func: rpcSetJigglerState, Params: []string{"enabled"}},
	"getJigglerState":        {Func: rpcGetJigglerState},
	"sendWOLMagicPacket":     {Func: rpcSendWOLMagicPacket, Params: []string{"macAddress"}},
	"getStreamQualityFactor": {Func: rpcGetStreamQualityFactor},
	"setStreamQualityFactor": {Func: rpcSetStreamQualityFactor, Params: []string{"factor"}},
	"getAutoUpdateState":     {Func: rpcGetAutoUpdateState},
	"setAutoUpdateState":     {Func: rpcSetAutoUpdateState, Params: []string{"enabled"}},
	"getEDID":                {Func: rpcGetEDID},
	"setEDID":                {Func: rpcSetEDID, Params: []string{"edid"}},
	"getDevChannelState":     {Func: rpcGetDevChannelState},
	"setDevChannelState":     {Func: rpcSetDevChannelState, Params: []string{"enabled"}},
	"getUpdateStatus":        {Func: rpcGetUpdateStatus},
	"tryUpdate":              {Func: rpcTryUpdate},
	"getDevModeState":        {Func: rpcGetDevModeState},
	"setDevModeState":        {Func: rpcSetDevModeState, Params: []string{"enabled"}},
	"getSSHKeyState":         {Func: rpcGetSSHKeyState},
	"setSSHKeyState":         {Func: rpcSetSSHKeyState, Params: []string{"sshKey"}},
	"setMassStorageMode":     {Func: rpcSetMassStorageMode, Params: []string{"mode"}},
	"getMassStorageMode":     {Func: rpcGetMassStorageMode},
	"isUpdatePending":        {Func: rpcIsUpdatePending},
	"getUsbEmulationState":   {Func: rpcGetUsbEmulationState},
	"setUsbEmulationState":   {Func: rpcSetUsbEmulationState, Params: []string{"enabled"}},
	"checkMountUrl":          {Func: rpcCheckMountUrl, Params: []string{"url"}},
	"getVirtualMediaState":   {Func: rpcGetVirtualMediaState},
	"getStorageSpace":        {Func: rpcGetStorageSpace},
	"mountWithHTTP":          {Func: rpcMountWithHTTP, Params: []string{"url", "mode"}},
	"mountWithWebRTC":        {Func: rpcMountWithWebRTC, Params: []string{"filename", "size", "mode"}},
	"mountWithStorage":       {Func: rpcMountWithStorage, Params: []string{"filename", "mode"}},
	"listStorageFiles":       {Func: rpcListStorageFiles},
	"deleteStorageFile":      {Func: rpcDeleteStorageFile, Params: []string{"filename"}},
	"startStorageFileUpload": {Func: rpcStartStorageFileUpload, Params: []string{"filename", "size"}},
	"getWakeOnLanDevices":    {Func: rpcGetWakeOnLanDevices},
	"setWakeOnLanDevices":    {Func: rpcSetWakeOnLanDevices, Params: []string{"params"}},
	"resetConfig":            {Func: rpcResetConfig},
	"setBacklightSettings":   {Func: rpcSetBacklightSettings, Params: []string{"params"}},
	"getBacklightSettings":   {Func: rpcGetBacklightSettings},
	"getDCPowerState":        {Func: rpcGetDCPowerState},
	"setDCPowerState":        {Func: rpcSetDCPowerState, Params: []string{"enabled"}},
	"getActiveExtension":     {Func: rpcGetActiveExtension},
	"setActiveExtension":     {Func: rpcSetActiveExtension, Params: []string{"extensionId"}},
	"getATXState":            {Func: rpcGetATXState},
	"setATXPowerAction":      {Func: rpcSetATXPowerAction, Params: []string{"action"}},
	"getSerialSettings":      {Func: rpcGetSerialSettings},
	"setSerialSettings":      {Func: rpcSetSerialSettings, Params: []string{"settings"}},
}
