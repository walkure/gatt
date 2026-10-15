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
		return newSocket(fd, uint16(n), chk)
	}

	req := devListRequest{devNum: hciMaxDevices}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceList, uintptr(unsafe.Pointer(&req))); err != nil {
		return nil, fmt.Errorf("hciGetDeviceList failed: %w", err)
	}
	logger.Debugf("got %d devices", req.devNum)
	if req.devNum == 0 {
		return nil,errors.New("no supported devices found")
	}
	errs := make([]error, 0, int(req.devNum))
	for i := 0; i < int(req.devNum); i++ {
		logger.Debugf("Try to open device[%d]: id:%d",i,req.devRequest[i].id)
		d, err := newSocket(fd, req.devRequest[i].id, chk)
		if err == nil {
			logger.Debugf("dev: %s opened", d.name)
			return d, err
		} else {
			errs = append(errs, fmt.Errorf("error %d: %v", i, err))
		}
	}
	return nil, fmt.Errorf("no supported devices available: %w", errors.Join(errs...))
}

func newSocket(fd int , id uint16, chk bool) (*device, error) {
	i := hciDevInfo{id: id}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceInfo, uintptr(unsafe.Pointer(&i))); err != nil {
		return nil, fmt.Errorf("hciGetDeviceInfo failed: %w", err)
	}
	name := i.name.String()
	// Check the feature list returned feature list.
	if chk && i.features[4]&0x40 == 0 {
		return nil, fmt.Errorf("does not support LE. dev: %q", name)
	}
	logger.Debugf("dev: %s up", name)
	if err := gioctl.Ioctl(uintptr(fd), hciUpDevice, uintptr(id)); err != nil {
		if err != syscall.EALREADY {
			return nil, err
		}
		logger.Debugf("dev: %s reset", name)
		if err := gioctl.Ioctl(uintptr(fd), hciResetDevice, uintptr(id)); err != nil {
			return nil, fmt.Errorf("hciResetDevice failed: %w", err)
		}
	}
	logger.Debugf("dev: %s down", name)
	if err := gioctl.Ioctl(uintptr(fd), hciDownDevice, uintptr(id)); err != nil {
		return nil, fmt.Errorf("hciDownDevice failed: %w", err)
	}

	// Attempt to use the linux 3.14 feature, if this fails with EINVAL fall back to raw access
	// on older kernels.
	sa := socket.SockaddrHCI{Dev: int(id), Channel: socket.HCI_CHANNEL_USER}
	if err := socket.Bind(fd, &sa); err != nil {
		if err != syscall.EINVAL {
			return nil, fmt.Errorf("dev %q doesn't returns EINVAL: %w", name, err)
		}
		logger.Warnf("dev: %q can't bind to hci user channel, err: %s.", name, err)

		// Pre-3.14 kernel fallback. Unlike HCI_CHANNEL_USER,
		// HCI_CHANNEL_RAW requires the device to already be HCI_UP --
		// hci_sock_sendmsg() returns ENETDOWN otherwise -- which is the
		// opposite of what the HCIDEVDOWN call above just did. Bring it
		// back up. EALREADY here just means something else (e.g. a
		// device-specific bring-up helper that already did the kernel's
		// HCI_AUTO_OFF dance via an mgmt Set Powered command -- see
		// doc/bluetooth.md section 16 in the walkure/nextdrive repo for
		// why that's needed on some hardware) already brought it up; in
		// that case leave it alone rather than resetting/downing it.
		if err := gioctl.Ioctl(uintptr(fd), hciUpDevice, uintptr(id)); err != nil && err != syscall.EALREADY {
			return nil, fmt.Errorf("hciUpDevice (raw channel fallback) failed: %w", err)
		}

		sa := socket.SockaddrHCI{Dev: int(id), Channel: socket.HCI_CHANNEL_RAW}
		if err := socket.Bind(fd, &sa); err != nil {
			return nil, fmt.Errorf("dev: %q can't bind to hci raw channel, err: %w", name, err)
		}

		// Unlike HCI_CHANNEL_USER, HCI_CHANNEL_RAW silently drops every
		// packet unless this socket's filter has the matching type/event
		// bits set (hci_send_to_sock() in the kernel; see doc/bluetooth.md
		// section 17 for how this was diagnosed -- cmd_tx/evt_rx counters
		// kept incrementing in hcidev_stats while reads blocked forever,
		// because the raw socket never actually got the matching events).
		flt := socket.HCIFilter{TypeMask: 1 << 4 /* HCI_EVENT_PKT */}
		flt.EventMask[0] = 0xffffffff
		flt.EventMask[1] = 0xffffffff
		if err := socket.SetsockoptFilter(fd, &flt); err != nil {
			return nil, fmt.Errorf("dev %q: setsockopt(HCI_FILTER) failed: %w", name, err)
		}
	}

	fds := make([]unix.PollFd, 1)
	fds[0].Fd = int32(fd)
	fds[0].Events = unix.POLLIN

	return &device{
		fd:   fd,
		fds:  fds,
		dev:  int(id),
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
