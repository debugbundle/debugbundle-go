//go:build unix

package transport

import "syscall"

const openNoFollowFlag = syscall.O_NOFOLLOW
