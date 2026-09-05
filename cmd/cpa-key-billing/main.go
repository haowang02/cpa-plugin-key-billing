//go:build cshared

// Command cpa-key-billing is the C ABI entry point of the CLIProxyAPI plugin.
// It is built with -buildmode=c-shared and contains no logic beyond marshalling
// between the C boundary and internal/plugin.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unsafe"

	"cpa-key-billing/internal/plugin"
)

var app = plugin.NewApp()

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if api == nil {
		return 1
	}
	C.store_host_api(host)
	app.SetHostCaller(callHost)
	api.abi_version = C.uint32_t(plugin.ABIVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("编码宿主调用 %s 请求失败：%w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		request := C.CBytes(rawPayload)
		if request == nil {
			return nil, fmt.Errorf("分配宿主调用 %s 请求内存失败", method)
		}
		defer C.free(request)
		requestPtr = (*C.uint8_t)(request)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	if response.ptr != nil {
		defer C.free_host_buffer(response.ptr, response.len)
	}
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		if response.len > C.size_t(^uint32(0)>>1) {
			return nil, fmt.Errorf("宿主调用 %s 响应过大", method)
		}
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("宿主调用 %s 无响应，code=%d", method, int(callCode))
	}
	var envelope plugin.Envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("解析宿主调用 %s 响应失败：%w", method, errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return nil, fmt.Errorf("%s：%s", envelope.Error.Code, envelope.Error.Message)
		}
		return nil, fmt.Errorf("宿主调用 %s 失败", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("宿主调用 %s 返回 code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), envelope.Result...), nil
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil {
		writeResponse(response, plugin.ErrorEnvelope("invalid_method", "缺少插件方法", http.StatusBadRequest))
		return 1
	}
	methodName := C.GoString(method)
	var requestBytes []byte
	if request == nil && requestLen > 0 {
		writeResponse(response, plugin.ErrorEnvelope("invalid_request", "插件请求指针为空", http.StatusBadRequest))
		return 1
	}
	if request != nil && requestLen > 0 {
		length := C.int(requestLen)
		if length < 0 || C.size_t(length) != requestLen {
			writeResponse(response, plugin.ErrorEnvelope("invalid_request", "插件请求过大", http.StatusBadRequest))
			return 1
		}
		requestBytes = C.GoBytes(unsafe.Pointer(request), length)
	}
	raw, errHandle := app.HandleMethod(methodName, requestBytes)
	if errHandle != nil {
		writeResponse(response, plugin.ErrorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	// Response buffers are allocated with C.CBytes, so they are freed here.
	_ = length
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	app.Shutdown()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
