package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The native helper runs unchanged against controlled API libraries. Linux
// uses real GLib variants and a fake GIO bus; no credential service is contacted.
func TestMemoryOSNativeAPI(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 unavailable")
	}
	for _, backend := range []string{"keychain", "secret-service"} {
		if backend == "secret-service" && runtime.GOOS != "linux" {
			continue
		}
		if backend == "secret-service" {
			if err := exec.Command(python, "-I", "-c", "import ctypes; ctypes.CDLL('libglib-2.0.so.0')").Run(); err != nil {
				t.Log("skipping native Secret Service binding cases: optional GLib unavailable")
				continue
			}
		}
		for _, op := range []string{"write", "read", "delete"} {
			for _, fault := range []string{"", "locked", "prompt-required", "no-collection", "locked-item", "interaction-denied"} {
				if (backend == "keychain" && (fault == "no-collection" || fault == "locked-item")) ||
					(backend == "secret-service" && fault == "interaction-denied") ||
					(op == "write" && fault == "locked-item") {
					continue
				}
				t.Run(backend+"/"+op+"/"+fault, func(t *testing.T) {
					value := []byte("'\"\\`$();deadbeef")
					cmd := exec.Command(python, "-I", "-c", testMemoryNativeAPI+memoryOSScript,
						backend, op, memoryOSName(strings.Repeat("a", 32)))
					cmd.Env = append(os.Environ(), "PYRY_NATIVE_FAULT="+fault, "PYRY_NATIVE_VALUE="+hex.EncodeToString(value))
					cmd.Stdin = bytes.NewReader(value)
					out, err := cmd.CombinedOutput()
					if fault != "" {
						testMemoryCheck(t, err != nil && len(out) == 0, "locked/prompt-required API invoked interaction or succeeded")
					} else {
						testMemoryMust(t, err)
						want := []byte(nil)
						if op == "read" {
							want = value
						}
						testMemoryCheck(t, bytes.Equal(out, want), "native API changed bytes or emitted diagnostics")
					}
				})
			}
		}
		for _, value := range []string{"deadbeef", "\x00binary", "\x01control", "trailing\n", "\x7f", "\xff", strings.Repeat("x", 4096)} {
			t.Run(backend+"/raw/"+hex.EncodeToString([]byte(value[:1])), func(t *testing.T) {
				cmd := exec.Command(python, "-I", "-c", testMemoryNativeAPI+memoryOSScript,
					backend, "read", memoryOSName(strings.Repeat("a", 32)))
				cmd.Env = append(os.Environ(), "PYRY_NATIVE_VALUE="+hex.EncodeToString([]byte(value)))
				out, err := cmd.CombinedOutput()
				testMemoryMust(t, err)
				testMemoryCheck(t, string(out) == value, "native read rendered stored bytes")
			})
		}
	}
}

func TestMemoryOSStoredBytes(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s, root := testMemoryOS(t, platform)
			ref := testMemorySet(t, s, memoryOld)
			_, sel := testMemorySelection(t, s)
			path := filepath.Join(root, memoryOSName(sel.Generation))
			for _, tc := range []struct {
				value string
				valid bool
			}{
				{"deadbeef0123456789", true}, {"'\"\\`$();&|<>!", true},
				{"\x00binary", false}, {"\x01control", false}, {"trailing\n", false},
				{"\x7f", false}, {"\xff", false},
			} {
				value := tc.value
				testMemoryMust(t, os.WriteFile(path, []byte(value), 0600))
				got, err := s.resolve(context.Background(), ref)
				ok, statusErr := s.status(context.Background())
				if !tc.valid {
					testMemoryReject(t, err)
					testMemoryReject(t, statusErr)
					testMemoryCheck(t, got == "", "invalid stored bytes resolved")
				} else {
					testMemoryMust(t, err)
					testMemoryMust(t, statusErr)
					testMemoryCheck(t, ok && got == value, "valid stored bytes changed")
				}
			}
			testMemoryMust(t, os.WriteFile(path, []byte(memoryOld), 0600))
			testMemoryResolve(t, s, ref, memoryOld)
		})
	}
}

const testMemoryNativeAPI = `
import ctypes as C
import os
import sys
import re

fault = os.environ.get('PYRY_NATIVE_FAULT', '')
raw = bytes.fromhex(os.environ['PYRY_NATIVE_VALUE'])
load = C.CDLL
buffers = []

class Fake:
    def __init__(self, functions):
        self.functions = functions
    def __getattr__(self, name):
        def invoke(*args):
            return self.functions[name](*args)
        return invoke

allowed = True
def interaction(flag):
    global allowed
    allowed = bool(flag)
    return -25308 if fault == 'interaction-denied' else 0

def accessible():
    if allowed:
        sys.stdout.write('PROMPT')
        raise RuntimeError()
    return fault not in ('locked', 'prompt-required')

def find(keychain, n, name, a, account, size, data, item):
    assert name == sys.argv[3].encode() and account == b'pyry-memory'
    if not accessible():
        return -25308
    item._obj.value = 1
    if size:
        b = C.create_string_buffer(raw)
        buffers.append(b)
        size._obj.value, data._obj.value = len(raw), C.addressof(b)
    return 0

def add(keychain, n, name, a, account, length, value, item):
    assert name == sys.argv[3].encode() and account == b'pyry-memory'
    assert C.string_at(value, length) == raw
    return 0 if accessible() else -25308

security = Fake({'SecKeychainSetUserInteractionAllowed': interaction,
                 'SecKeychainFindGenericPassword': find,
                 'SecKeychainAddGenericPassword': add,
                 'SecKeychainItemDelete': lambda item: 0 if accessible() else -25308,
                 'SecKeychainItemFreeContent': lambda *args: 0})
cf = Fake({'CFRelease': lambda item: None})

if sys.argv[1] == 'secret-service':
    glib = load('libglib-2.0.so.0')
    parse = glib.g_variant_parse
    parse.restype, parse.argtypes = C.c_void_p, [C.c_void_p, C.c_char_p, C.c_void_p, C.c_void_p, C.c_void_p]
    printer = glib.g_variant_print
    printer.restype, printer.argtypes = C.c_void_p, [C.c_void_p, C.c_int]
    free = glib.g_free
    free.argtypes = [C.c_void_p]
    kind = glib.g_variant_get_type_string
    kind.restype, kind.argtypes = C.c_char_p, [C.c_void_p]

    def reply(text):
        v = parse(None, text.encode(), None, None, None)
        assert v
        return v

    def call(bus, destination, path, interface, method, params, rtype, flags, timeout, cancel, error):
        assert destination == b'org.freedesktop.secrets' and timeout <= 10000
        p = printer(params, 1) if params else None
        text = C.string_at(p).decode() if p else ''
        if p:
            free(p)
        if method in (b'Prompt', b'Unlock', b'CreateCollection'):
            sys.stdout.write('PROMPT')
            raise RuntimeError()
        if method == b'ReadAlias':
            assert text == "('default',)"
            return reply("(objectpath '%s',)" % ('/' if fault == 'no-collection' else '/collection'))
        if method == b'Get':
            assert 'Locked' in text
            locked = fault == 'locked' or (fault == 'locked-item' and path == b'/item')
            return reply('(<%s>,)' % ('true' if locked else 'false'))
        if method == b'SearchItems':
            assert sys.argv[3] in text
            return reply('(@ao [],)' if sys.argv[2] == 'write' else "([objectpath '/item'],)")
        if method == b'OpenSession':
            assert text == "('plain', <''>)"
            return reply("(<''>, objectpath '/session')")
        if method == b'CreateItem':
            assert kind(params) == b'(a{sv}(oayays)b)' and text.endswith('false)')
            assert sys.argv[3] in text
            assert bytes(int(b, 16) for b in re.findall(r'0x([0-9a-f]{2})', text)) == raw
            return reply("(objectpath '%s', objectpath '%s')" %
                         (('/', '/prompt') if fault == 'prompt-required' else ('/item', '/')))
        if method == b'GetSecret':
            if fault == 'prompt-required':
                return None
            value = '@ay [' + ','.join('0x%02x' % b for b in raw) + ']'
            return reply("((objectpath '/session', @ay [], " + value + ", 'text/plain'),)")
        if method == b'Delete':
            return reply("(objectpath '%s',)" % ('/prompt' if fault == 'prompt-required' else '/'))
        assert method == b'Close'
        return reply('()')

    gio = Fake({'g_bus_get_sync': lambda *args: 1,
                'g_dbus_connection_call_sync': call,
                'g_object_unref': lambda bus: None})

def controlled_library(path):
    if 'Security.framework' in path:
        return security
    if 'CoreFoundation.framework' in path:
        return cf
    if path == 'libgio-2.0.so.0':
        return gio
    return load(path)
C.CDLL = controlled_library
`
