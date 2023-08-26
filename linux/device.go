package linux

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/walkure/gatt/linux/gioctl"
	"github.com/walkure/gatt/linux/socket"
	"github.com/walkure/gatt/logger"
	"golang.org/x/sys/unix"
)

type device struct {
	fd   int
	fds  []unix.PollFd
	dev  int
	name string
	rmu  *sync.Mutex
	wmu  *sync.Mutex
}

func newDevice(n int, chk bool) (*device, error) {
	fd, err := socket.Socket(socket.AF_BLUETOOTH, syscall.SOCK_RAW, socket.BTPROTO_HCI)
	if err != nil {
		return nil, fmt.Errorf("could not create AF_BLUETOOTH raw socket: %w", err)
	}
	if n != -1 {
		return newSocket(fd, n, chk)
	}

	req := devListRequest{devNum: hciMaxDevices}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceList, uintptr(unsafe.Pointer(&req))); err != nil {
		return nil, fmt.Errorf("hciGetDeviceList failed: %w", err)
	}
	logger.Debugf("got %d devices", req.devNum)
	errs := make([]error, 0, int(req.devNum))
	for i := 0; i < int(req.devNum); i++ {
		d, err := newSocket(fd, i, chk)
		if err == nil {
			logger.Debugf("dev: %s opened", d.name)
			return d, err
		} else {
			errs = append(errs, fmt.Errorf("error %d: %v", i, err))
		}
	}
	return nil, fmt.Errorf("no supported devices available: %w", errors.Join(errs...))
}

func newSocket(fd, n int, chk bool) (*device, error) {
	i := hciDevInfo{id: uint16(n)}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceInfo, uintptr(unsafe.Pointer(&i))); err != nil {
		return nil, fmt.Errorf("hciGetDeviceInfo failed: %w", err)
	}
	name := string(i.name[:])
	// Check the feature list returned feature list.
	if chk && i.features[4]&0x40 == 0 {
		return nil, fmt.Errorf("does not support LE. dev: %q", name)
	}
	logger.Debugf("dev: %s up", name)
	if err := gioctl.Ioctl(uintptr(fd), hciUpDevice, uintptr(n)); err != nil {
		if err != syscall.EALREADY {
			return nil, err
		}
		logger.Debugf("dev: %s reset", name)
		if err := gioctl.Ioctl(uintptr(fd), hciResetDevice, uintptr(n)); err != nil {
			return nil, fmt.Errorf("hciResetDevice failed: %w", err)
		}
	}
	logger.Debugf("dev: %s down", name)
	if err := gioctl.Ioctl(uintptr(fd), hciDownDevice, uintptr(n)); err != nil {
		return nil, fmt.Errorf("hciDownDevice failed: %w", err)
	}

	// Attempt to use the linux 3.14 feature, if this fails with EINVAL fall back to raw access
	// on older kernels.
	sa := socket.SockaddrHCI{Dev: n, Channel: socket.HCI_CHANNEL_USER}
	if err := socket.Bind(fd, &sa); err != nil {
		if err != syscall.EINVAL {
			return nil, fmt.Errorf("dev %q doesn't returns EINVAL: %w", name, err)
		}
		logger.Warnf("dev: %q can't bind to hci user channel, err: %s.", name, err)
		sa := socket.SockaddrHCI{Dev: n, Channel: socket.HCI_CHANNEL_RAW}
		if err := socket.Bind(fd, &sa); err != nil {
			return nil, fmt.Errorf("dev: %q can't bind to hci raw channel, err: %w", name, err)
		}
	}

	fds := make([]unix.PollFd, 1)
	fds[0].Fd = int32(fd)
	fds[0].Events = unix.POLLIN

	return &device{
		fd:   fd,
		fds:  fds,
		dev:  n,
		name: name,
		rmu:  &sync.Mutex{},
		wmu:  &sync.Mutex{},
	}, nil
}

func (d device) Read(b []byte) (int, error) {
	d.rmu.Lock()
	defer d.rmu.Unlock()
	// Use poll to avoid blocking on Read
	n, err := unix.Poll(d.fds, 100)
	if n == 0 || err != nil {
		return 0, err
	}
	return syscall.Read(d.fd, b)
}

func (d device) Write(b []byte) (int, error) {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return syscall.Write(d.fd, b)
}

func (d device) Close() error {
	logger.Debugf("linux.device.Close()")
	return syscall.Close(d.fd)
}
