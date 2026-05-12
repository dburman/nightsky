//go:build zwo

// Package zwo provides CGo bindings to the ZWO ASI camera SDK (libASICamera2).
//
// Build with: go build -tags zwo
// Requires: libASICamera2.so and libusb-1.0 installed on the system.
package zwo

/*
#cgo LDFLAGS: -lASICamera2 -lusb-1.0
#cgo CFLAGS: -I/usr/include/libasi

#include <stdlib.h>
#include <string.h>

// ASI SDK type definitions (from ASICamera2.h)
typedef int ASI_BOOL;

typedef enum {
	ASI_IMG_RAW8 = 0,
	ASI_IMG_RGB24,
	ASI_IMG_RAW16,
	ASI_IMG_Y8,
	ASI_IMG_END = -1
} ASI_IMG_TYPE;

typedef enum {
	ASI_GAIN = 0,
	ASI_EXPOSURE,
	ASI_GAMMA,
	ASI_WB_R,
	ASI_WB_B,
	ASI_OFFSET,
	ASI_BANDWIDTHOVERLOAD,
	ASI_OVERCLOCK,
	ASI_TEMPERATURE,
	ASI_FLIP,
	ASI_AUTO_MAX_GAIN,
	ASI_AUTO_MAX_EXP,
	ASI_AUTO_TARGET_BRIGHTNESS,
	ASI_HARDWARE_BIN,
	ASI_HIGH_SPEED_MODE,
	ASI_COOLER_POWER_PERC,
	ASI_TARGET_TEMP,
	ASI_COOLER_ON,
	ASI_MONO_BIN,
	ASI_FAN_ON,
	ASI_PATTERN_ADJUST,
	ASI_ANTI_DEW_HEATER
} ASI_CONTROL_TYPE;

typedef enum {
	ASI_FALSE = 0,
	ASI_TRUE
} ASI_BOOL_ENUM;

typedef enum {
	ASI_EXP_IDLE = 0,
	ASI_EXP_WORKING,
	ASI_EXP_SUCCESS,
	ASI_EXP_FAILED
} ASI_EXPOSURE_STATUS;

typedef enum {
	ASI_SUCCESS = 0,
	ASI_ERROR_INVALID_INDEX,
	ASI_ERROR_INVALID_ID,
	ASI_ERROR_INVALID_CONTROL_TYPE,
	ASI_ERROR_CAMERA_CLOSED,
	ASI_ERROR_CAMERA_REMOVED,
	ASI_ERROR_INVALID_PATH,
	ASI_ERROR_INVALID_FILEFORMAT,
	ASI_ERROR_INVALID_SIZE,
	ASI_ERROR_INVALID_IMGTYPE,
	ASI_ERROR_OUTOF_BOUNDARY,
	ASI_ERROR_TIMEOUT,
	ASI_ERROR_INVALID_SEQUENCE,
	ASI_ERROR_BUFFER_TOO_SMALL,
	ASI_ERROR_VIDEO_MODE_ACTIVE,
	ASI_ERROR_EXPOSURE_IN_PROGRESS,
	ASI_ERROR_GENERAL_ERROR,
	ASI_ERROR_INVALID_MODE,
	ASI_ERROR_END
} ASI_ERROR_CODE;

typedef struct {
	char Name[64];
	int CameraID;
	long MaxHeight;
	long MaxWidth;
	ASI_BOOL IsColorCam;
	int BayerPattern;
	int SupportedBins[16];
	ASI_IMG_TYPE SupportedVideoFormat[8];
	double PixelSize;
	ASI_BOOL MechanicalShutter;
	ASI_BOOL ST4Port;
	ASI_BOOL IsCoolerCam;
	ASI_BOOL IsUSB3Host;
	ASI_BOOL IsUSB3Camera;
	float ElecPerADU;
	int BitDepth;
	ASI_BOOL IsTriggerCam;
	char Unused[16];
} ASI_CAMERA_INFO;

typedef struct {
	char Name[64];
	char Description[128];
	long MaxValue;
	long MinValue;
	long DefaultValue;
	ASI_BOOL IsAutoSupported;
	ASI_BOOL IsWritable;
	ASI_CONTROL_TYPE ControlType;
	char Unused[32];
} ASI_CONTROL_CAPS;

// External ASI SDK functions
extern int ASIGetNumOfConnectedCameras();
extern ASI_ERROR_CODE ASIGetCameraProperty(ASI_CAMERA_INFO *pASICameraInfo, int iCameraIndex);
extern ASI_ERROR_CODE ASIOpenCamera(int iCameraID);
extern ASI_ERROR_CODE ASIInitCamera(int iCameraID);
extern ASI_ERROR_CODE ASICloseCamera(int iCameraID);
extern ASI_ERROR_CODE ASIGetNumOfControls(int iCameraID, int *piNumberOfControls);
extern ASI_ERROR_CODE ASIGetControlCaps(int iCameraID, int iControlIndex, ASI_CONTROL_CAPS *pControlCaps);
extern ASI_ERROR_CODE ASIGetControlValue(int iCameraID, ASI_CONTROL_TYPE ControlType, long *plValue, ASI_BOOL *pbAuto);
extern ASI_ERROR_CODE ASISetControlValue(int iCameraID, ASI_CONTROL_TYPE ControlType, long lValue, ASI_BOOL bAuto);
extern ASI_ERROR_CODE ASISetROIFormat(int iCameraID, int iWidth, int iHeight, int iBin, ASI_IMG_TYPE Img_type);
extern ASI_ERROR_CODE ASIGetROIFormat(int iCameraID, int *piWidth, int *piHeight, int *piBin, ASI_IMG_TYPE *pImg_type);
extern ASI_ERROR_CODE ASIStartExposure(int iCameraID, ASI_BOOL bIsDark);
extern ASI_ERROR_CODE ASIStopExposure(int iCameraID);
extern ASI_ERROR_CODE ASIGetExpStatus(int iCameraID, ASI_EXPOSURE_STATUS *pExpStatus);
extern ASI_ERROR_CODE ASIGetDataAfterExp(int iCameraID, unsigned char *pBuf, long lBufSize);
extern ASI_ERROR_CODE ASIStartVideoCapture(int iCameraID);
extern ASI_ERROR_CODE ASIStopVideoCapture(int iCameraID);
extern ASI_ERROR_CODE ASIGetVideoData(int iCameraID, unsigned char *pBuf, long lBufSize, int iWait_ms);
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// ASI SDK error codes mapped to Go.
type ASIError int

const (
	ASISuccess           ASIError = C.ASI_SUCCESS
	ASIErrorInvalidIndex ASIError = C.ASI_ERROR_INVALID_INDEX
	ASIErrorInvalidID    ASIError = C.ASI_ERROR_INVALID_ID
	ASIErrorTimeout      ASIError = C.ASI_ERROR_TIMEOUT
	ASIErrorGeneral      ASIError = C.ASI_ERROR_GENERAL_ERROR
)

func (e ASIError) Error() string {
	switch e {
	case ASISuccess:
		return "success"
	case ASIErrorInvalidIndex:
		return "invalid camera index"
	case ASIErrorInvalidID:
		return "invalid camera ID"
	case ASIErrorTimeout:
		return "exposure timeout"
	case ASIErrorGeneral:
		return "general error"
	default:
		return fmt.Sprintf("ASI error %d", int(e))
	}
}

func asiCheck(code C.ASI_ERROR_CODE) error {
	if code == C.ASI_SUCCESS {
		return nil
	}
	return ASIError(code)
}

// ASI image type constants.
const (
	ImgRAW8  = C.ASI_IMG_RAW8
	ImgRGB24 = C.ASI_IMG_RGB24
	ImgRAW16 = C.ASI_IMG_RAW16
	ImgY8    = C.ASI_IMG_Y8
)

// ASI control type constants.
const (
	CtrlGain               = C.ASI_GAIN
	CtrlExposure           = C.ASI_EXPOSURE
	CtrlGamma              = C.ASI_GAMMA
	CtrlWBR                = C.ASI_WB_R
	CtrlWBB                = C.ASI_WB_B
	CtrlOffset             = C.ASI_OFFSET
	CtrlBandwidth          = C.ASI_BANDWIDTHOVERLOAD
	CtrlTemperature        = C.ASI_TEMPERATURE
	CtrlFlip               = C.ASI_FLIP
	CtrlAutoMaxGain        = C.ASI_AUTO_MAX_GAIN
	CtrlAutoMaxExp         = C.ASI_AUTO_MAX_EXP
	CtrlHighSpeedMode      = C.ASI_HIGH_SPEED_MODE
	CtrlCoolerPowerPercent = C.ASI_COOLER_POWER_PERC
	CtrlTargetTemp         = C.ASI_TARGET_TEMP
	CtrlCoolerOn           = C.ASI_COOLER_ON
)

// Exposure status constants.
const (
	ExpIdle    = C.ASI_EXP_IDLE
	ExpWorking = C.ASI_EXP_WORKING
	ExpSuccess = C.ASI_EXP_SUCCESS
	ExpFailed  = C.ASI_EXP_FAILED
)

// Bayer pattern constants.
const (
	BayerRG = 0
	BayerBG = 1
	BayerGR = 2
	BayerGB = 3
)

// CameraProperties holds the properties returned by the ASI SDK for a camera.
type CameraProperties struct {
	Name              string
	CameraID          int
	MaxWidth          int
	MaxHeight         int
	IsColor           bool
	BayerPattern      int
	SupportedBins     []int
	PixelSize         float64
	HasCooler         bool
	IsUSB3            bool
	BitDepth          int
	ElectronsPerADU   float64
	HasST4            bool
}

// ControlCaps describes the capabilities of a single camera control.
type ControlCaps struct {
	Name            string
	Description     string
	ControlType     int
	MinValue        int64
	MaxValue        int64
	DefaultValue    int64
	IsAutoSupported bool
	IsWritable      bool
}

// GetNumConnected returns the number of connected ASI cameras.
func GetNumConnected() int {
	return int(C.ASIGetNumOfConnectedCameras())
}

// GetCameraProperties returns properties for the camera at the given index.
func GetCameraProperties(index int) (CameraProperties, error) {
	var info C.ASI_CAMERA_INFO
	if err := asiCheck(C.ASIGetCameraProperty(&info, C.int(index))); err != nil {
		return CameraProperties{}, fmt.Errorf("get camera property: %w", err)
	}

	props := CameraProperties{
		Name:            C.GoString(&info.Name[0]),
		CameraID:        int(info.CameraID),
		MaxWidth:        int(info.MaxWidth),
		MaxHeight:       int(info.MaxHeight),
		IsColor:         info.IsColorCam != 0,
		BayerPattern:    int(info.BayerPattern),
		PixelSize:       float64(info.PixelSize),
		HasCooler:       info.IsCoolerCam != 0,
		IsUSB3:          info.IsUSB3Camera != 0,
		BitDepth:        int(info.BitDepth),
		ElectronsPerADU: float64(info.ElecPerADU),
		HasST4:          info.ST4Port != 0,
	}

	// Extract supported bins (terminated by 0).
	for i := 0; i < 16; i++ {
		b := int(info.SupportedBins[i])
		if b == 0 {
			break
		}
		props.SupportedBins = append(props.SupportedBins, b)
	}

	return props, nil
}

// OpenCamera opens the camera with the given ID.
func OpenCamera(id int) error {
	return asiCheck(C.ASIOpenCamera(C.int(id)))
}

// InitCamera initializes the camera after opening.
func InitCamera(id int) error {
	return asiCheck(C.ASIInitCamera(C.int(id)))
}

// CloseCamera closes the camera.
func CloseCamera(id int) error {
	return asiCheck(C.ASICloseCamera(C.int(id)))
}

// GetNumControls returns the number of controls for the camera.
func GetNumControls(id int) (int, error) {
	var n C.int
	if err := asiCheck(C.ASIGetNumOfControls(C.int(id), &n)); err != nil {
		return 0, err
	}
	return int(n), nil
}

// GetControlCaps returns the capabilities of a control by index.
func GetControlCaps(cameraID, controlIndex int) (ControlCaps, error) {
	var caps C.ASI_CONTROL_CAPS
	if err := asiCheck(C.ASIGetControlCaps(C.int(cameraID), C.int(controlIndex), &caps)); err != nil {
		return ControlCaps{}, err
	}

	return ControlCaps{
		Name:            C.GoString(&caps.Name[0]),
		Description:     C.GoString(&caps.Description[0]),
		ControlType:     int(caps.ControlType),
		MinValue:        int64(caps.MinValue),
		MaxValue:        int64(caps.MaxValue),
		DefaultValue:    int64(caps.DefaultValue),
		IsAutoSupported: caps.IsAutoSupported != 0,
		IsWritable:      caps.IsWritable != 0,
	}, nil
}

// GetControlValue reads the current value of a control.
func GetControlValue(cameraID int, controlType int) (int64, bool, error) {
	var value C.long
	var auto C.ASI_BOOL
	if err := asiCheck(C.ASIGetControlValue(C.int(cameraID), C.ASI_CONTROL_TYPE(controlType), &value, &auto)); err != nil {
		return 0, false, err
	}
	return int64(value), auto != 0, nil
}

// SetControlValue sets a control value. If auto is true, the camera will auto-adjust.
func SetControlValue(cameraID int, controlType int, value int64, auto bool) error {
	var autoFlag C.ASI_BOOL
	if auto {
		autoFlag = C.ASI_TRUE
	}
	return asiCheck(C.ASISetControlValue(C.int(cameraID), C.ASI_CONTROL_TYPE(controlType), C.long(value), autoFlag))
}

// SetROIFormat configures the capture resolution, binning, and pixel format.
func SetROIFormat(cameraID, width, height, bin int, imgType int) error {
	return asiCheck(C.ASISetROIFormat(C.int(cameraID), C.int(width), C.int(height), C.int(bin), C.ASI_IMG_TYPE(imgType)))
}

// GetROIFormat returns the current capture resolution, binning, and pixel format.
func GetROIFormat(cameraID int) (width, height, bin int, imgType int, err error) {
	var w, h, b C.int
	var img C.ASI_IMG_TYPE
	if err := asiCheck(C.ASIGetROIFormat(C.int(cameraID), &w, &h, &b, &img)); err != nil {
		return 0, 0, 0, 0, err
	}
	return int(w), int(h), int(b), int(img), nil
}

// StartExposure begins a snapshot exposure. Set isDark to true for dark frames.
func StartExposure(cameraID int, isDark bool) error {
	var dark C.ASI_BOOL
	if isDark {
		dark = C.ASI_TRUE
	}
	return asiCheck(C.ASIStartExposure(C.int(cameraID), dark))
}

// StopExposure aborts a running exposure.
func StopExposure(cameraID int) error {
	return asiCheck(C.ASIStopExposure(C.int(cameraID)))
}

// GetExposureStatus returns the current exposure status.
func GetExposureStatus(cameraID int) (int, error) {
	var status C.ASI_EXPOSURE_STATUS
	if err := asiCheck(C.ASIGetExpStatus(C.int(cameraID), &status)); err != nil {
		return 0, err
	}
	return int(status), nil
}

// GetDataAfterExp copies the image data from the camera buffer after a successful exposure.
func GetDataAfterExp(cameraID int, buf []byte) error {
	return asiCheck(C.ASIGetDataAfterExp(
		C.int(cameraID),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.long(len(buf)),
	))
}

// StartVideoCapture begins continuous video capture mode.
func StartVideoCapture(cameraID int) error {
	return asiCheck(C.ASIStartVideoCapture(C.int(cameraID)))
}

// StopVideoCapture stops continuous video capture mode.
func StopVideoCapture(cameraID int) error {
	return asiCheck(C.ASIStopVideoCapture(C.int(cameraID)))
}

// GetVideoData reads a frame from the video capture buffer.
// waitMs is the timeout in milliseconds (-1 = wait forever).
func GetVideoData(cameraID int, buf []byte, waitMs int) error {
	return asiCheck(C.ASIGetVideoData(
		C.int(cameraID),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.long(len(buf)),
		C.int(waitMs),
	))
}

// BayerPatternString returns a human-readable Bayer pattern name.
func BayerPatternString(pattern int) string {
	switch pattern {
	case BayerRG:
		return "RGGB"
	case BayerBG:
		return "BGGR"
	case BayerGR:
		return "GRBG"
	case BayerGB:
		return "GBRG"
	default:
		return "unknown"
	}
}
