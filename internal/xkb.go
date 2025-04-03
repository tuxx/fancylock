package internal

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// XKB constants
const (
	KeymapFormatTextV1 = 1
	ContextNoFlags     = 0
)

var (
	libxkbcommon           uintptr
	xkbContextNew          func(uint32) uintptr
	xkbKeymapNewFromString func(uintptr, []byte, uint32, uint32) uintptr
	xkbStateNew            func(uintptr) uintptr
	xkbStateKeyGetOneSym   func(uintptr, uint) uintptr
	xkbKeysymToUtf32       func(uint) uint
	xkbKeymapUnref         func(uintptr)
	xkbStateUnref          func(uintptr)
	xkbContextUnref        func(uintptr)
)

func init() {
	var err error
	libxkbcommon, err = purego.Dlopen("libxkbcommon.so", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		libxkbcommon, err = purego.Dlopen("libxkbcommon.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(fmt.Errorf("failed to load libxkbcommon: %v", err))
		}
	}

	purego.RegisterLibFunc(&xkbContextNew, libxkbcommon, "xkb_context_new")
	purego.RegisterLibFunc(&xkbKeymapNewFromString, libxkbcommon, "xkb_keymap_new_from_string")
	purego.RegisterLibFunc(&xkbStateNew, libxkbcommon, "xkb_state_new")
	purego.RegisterLibFunc(&xkbStateKeyGetOneSym, libxkbcommon, "xkb_state_key_get_one_sym")
	purego.RegisterLibFunc(&xkbKeysymToUtf32, libxkbcommon, "xkb_keysym_to_utf32")
	purego.RegisterLibFunc(&xkbKeymapUnref, libxkbcommon, "xkb_keymap_unref")
	purego.RegisterLibFunc(&xkbStateUnref, libxkbcommon, "xkb_state_unref")
	purego.RegisterLibFunc(&xkbContextUnref, libxkbcommon, "xkb_context_unref")
}

// XKB wrapper functions
func XkbContextNew(flags uint32) uintptr {
	return xkbContextNew(flags)
}

func XkbKeymapNewFromString(context uintptr, str string, format uint32, flags uint32) uintptr {
	return xkbKeymapNewFromString(context, []byte(str), format, flags)
}

func XkbStateNew(keymap uintptr) uintptr {
	return xkbStateNew(keymap)
}

func XkbStateKeyGetSym(state uintptr, key uint32) uint32 {
	return uint32(xkbStateKeyGetOneSym(state, uint(key)))
}

func XkbKeysymToUtf32(keysym uint32) uint32 {
	return uint32(xkbKeysymToUtf32(uint(keysym)))
}

func XkbKeymapUnref(keymap uintptr) {
	xkbKeymapUnref(keymap)
}

func XkbStateUnref(state uintptr) {
	xkbStateUnref(state)
}

func XkbContextUnref(context uintptr) {
	xkbContextUnref(context)
}
