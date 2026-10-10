package main

// memoryOSScript uses native APIs in an isolated, deadline-bound process. Secrets
// cross only stdin and raw read stdout. Missing Python/shared libraries are an
// unavailable backend, never a reason to switch an already committed selection.
const memoryOSScript = `
import ctypes as C
import json
import sys

P = C.c_void_p
S = C.c_char_p
U = C.c_uint32

def bind(lib, name, result, args):
    f = getattr(lib, name)
    f.restype, f.argtypes = result, args
    return f

def require(ok):
    if not ok:
        raise RuntimeError()

def keychain(op, name, secret):
    lib = C.CDLL('/System/Library/Frameworks/Security.framework/Security')
    cf = C.CDLL('/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation')
    interaction = bind(lib, 'SecKeychainSetUserInteractionAllowed', C.c_int32, [C.c_ubyte])
    require(interaction(0) == 0)
    name, account = name.encode('ascii'), b'pyry-memory'
    if op == 'write':
        add = bind(lib, 'SecKeychainAddGenericPassword', C.c_int32, [P, U, S, U, S, U, P, P])
        require(add(None, len(name), name, len(account), account, len(secret), secret, None) == 0)
        return b''
    find = bind(lib, 'SecKeychainFindGenericPassword', C.c_int32, [P, U, S, U, S, P, P, P])
    size, data, item = U(), P(), P()
    require(find(None, len(name), name, len(account), account,
                 C.byref(size) if op == 'read' else None,
                 C.byref(data) if op == 'read' else None, C.byref(item)) == 0)
    try:
        if op == 'delete':
            delete = bind(lib, 'SecKeychainItemDelete', C.c_int32, [P])
            require(delete(item) == 0)
            return b''
        require(size.value <= 4096)
        return C.string_at(data, size.value)
    finally:
        if data.value:
            bind(lib, 'SecKeychainItemFreeContent', C.c_int32, [P, P])(None, data)
        bind(cf, 'CFRelease', None, [P])(item)

def service(op, name, secret):
    glib = C.CDLL('libglib-2.0.so.0')
    gio = C.CDLL('libgio-2.0.so.0')
    parse = bind(glib, 'g_variant_parse', P, [P, S, P, P, P])
    unref = bind(glib, 'g_variant_unref', None, [P])
    kind = bind(glib, 'g_variant_get_type_string', S, [P])
    count = bind(glib, 'g_variant_n_children', C.c_size_t, [P])
    child = bind(glib, 'g_variant_get_child_value', P, [P, C.c_size_t])
    variant = bind(glib, 'g_variant_get_variant', P, [P])
    string = bind(glib, 'g_variant_get_string', S, [P, P])
    boolean = bind(glib, 'g_variant_get_boolean', C.c_int, [P])
    array = bind(glib, 'g_variant_get_fixed_array', P, [P, P, C.c_size_t])

    def unpack(v):
        try:
            t = kind(v)
            if t in (b's', b'o'):
                return string(v, None).decode('ascii')
            if t == b'b':
                return bool(boolean(v))
            if t == b'v':
                return unpack(variant(v))
            if t == b'ay':
                n = C.c_size_t()
                p = array(v, C.byref(n), 1)
                require(n.value <= 4096)
                return C.string_at(p, n.value)
            return [unpack(child(v, i)) for i in range(count(v))]
        finally:
            unref(v)

    bus = bind(gio, 'g_bus_get_sync', P, [C.c_int, P, P])(2, None, None)
    require(bus)
    call_sync = bind(gio, 'g_dbus_connection_call_sync', P,
                     [P, S, S, S, S, P, P, C.c_int, C.c_int, P, P])
    prefix = 'org.freedesktop.Secret.'
    root = '/org/freedesktop/secrets'

    def call(path, interface, method, text=None):
        params = parse(None, text.encode('ascii'), None, None, None) if text else None
        require(not text or params)
        try:
            reply = call_sync(bus, b'org.freedesktop.secrets', path.encode('ascii'),
                              interface.encode('ascii'), method.encode('ascii'),
                              params, None, 0, 10000, None, None)
            require(reply)
            return unpack(reply)
        finally:
            if params:
                unref(params)

    def unlocked(path, interface):
        require(not call(path, 'org.freedesktop.DBus.Properties', 'Get',
                         '(' + json.dumps(interface) + ', "Locked")')[0])

    session = None
    try:
        collection = call(root, prefix + 'Service', 'ReadAlias', "('default',)")[0]
        require(collection != '/')
        unlocked(collection, prefix + 'Collection')
        attrs = "{'service': " + json.dumps(name) + "}"
        items = call(collection, prefix + 'Collection', 'SearchItems', '(' + attrs + ',)')[0]
        if op == 'write':
            require(not items)
        else:
            require(len(items) == 1)
            unlocked(items[0], prefix + 'Item')
        if op == 'delete':
            require(call(items[0], prefix + 'Item', 'Delete')[0] == '/')
            return b''
        session = call(root, prefix + 'Service', 'OpenSession', "('plain', <''>)")[1]
        require(session != '/')
        if op == 'write':
            props = "{'" + prefix + "Item.Label': <'Pyry memory OpenAI'>, '" + prefix + "Item.Attributes': <" + attrs + ">}"
            value = '@ay [' + ','.join('0x%02x' % b for b in secret) + ']'
            item, prompt = call(collection, prefix + 'Collection', 'CreateItem',
                                '(' + props + ', (objectpath ' + json.dumps(session) +
                                ", @ay [], " + value + ", 'text/plain'), false)")
            require(prompt == '/' and item != '/')
            return b''
        result = call(items[0], prefix + 'Item', 'GetSecret', '(objectpath ' + json.dumps(session) + ',)')[0]
        require(result[0] == session and result[1] == b'')
        return result[2]
    finally:
        if session:
            call(session, prefix + 'Session', 'Close')
        bind(gio, 'g_object_unref', None, [P])(bus)

try:
    backend, op, name = sys.argv[1:]
    require(backend in ('keychain', 'secret-service') and op in ('write', 'read', 'delete'))
    require(name.startswith('pyry.memory.openai.') and len(name) == 51)
    require(all(c in '0123456789abcdef' for c in name[19:]))
    secret = sys.stdin.buffer.read(4097) if op == 'write' else b''
    require(len(secret) <= 4096)
    result = (keychain if backend == 'keychain' else service)(op, name, secret)
    if op == 'read':
        sys.stdout.buffer.write(result)
except Exception:
    sys.exit(1)
`
