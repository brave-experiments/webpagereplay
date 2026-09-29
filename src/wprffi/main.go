// Copyright 2026 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package main builds a c-shared native library (libwpr.so) that embeds
// WebPageReplay so hosts like Node.js can record and replay web traffic
// in-process via FFI, instead of spawning the wpr CLI.
//
// Build with:
//
//	go build -buildmode=c-shared -o libwpr.so .
//
// The C ABI consists of a single dispatch function and a free function.
// All strings are heap-allocated (C.CString) and MUST be freed by the
// caller via WPR_Free. Requests and responses are JSON; the schemas live
// in src/wprnative/session.go.
//
//	extern char* WPR_Call(const char* method, const char* requestJson);
//	extern void  WPR_Free(char* p);
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"unsafe"

	"go.chromium.org/webpagereplay/src/wprnative"
)

// WPR_Call dispatches a single WPR method ("start", "stop", "getArchive")
// with a JSON request body and returns a heap-allocated JSON response string.
// The returned string must be freed with WPR_Free. It never returns NULL.
//
//export WPR_Call
func WPR_Call(cMethod *C.char, cRequest *C.char) *C.char {
	var method string
	if cMethod != nil {
		method = C.GoString(cMethod)
	}
	var requestJSON []byte
	if cRequest != nil {
		requestJSON = []byte(C.GoString(cRequest))
	}
	response := wprnative.DispatchCall(method, requestJSON)
	return C.CString(string(response))
}

// WPR_Free frees a string previously returned by WPR_Call.
// Passing NULL is a no-op.
//
//export WPR_Free
func WPR_Free(p *C.char) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}

// Required by -buildmode=c-shared; never executed.
func main() {}
