# minimega live integration test topology: Android
#
# One KVM router and, launched by the tests, one Android Emulator VM on the
# same VLAN:
#
#   phones 192.168.20.0/24   rtr .1 (KVM, minirouter)   phone0 .2 (Android)
#
# Only the router is launched here. tests/live/android launches the phone from
# the configuration this file leaves behind, so a failed emulator launch is a
# test failure with the emulator log kept, not a failed "mm read".
#
# Requirements (staged by tests/live/run.sh):
#   - live.kernel and live.initrd in the minimega files directory, under live/
#   - the Android SDK and the AVD "mmtest", which are not in the image: run.sh
#     mounts them at the same path from the host
#     (LIVE_DOCKER_ARGS="-v /usr/local/lib/android:/usr/local/lib/android").
#     The paths, the AVD name and the hardware below match what the android
#     job in .github/workflows/live.yml installs.

namespace live

# --- KVM router: the phone's peer --------------------------------------------

clear vm config
vm config kernel live/live.kernel
vm config initrd live/live.initrd
vm config memory 448
vm config vcpus 1
vm config snapshot true
vm config append live.role=router live.hostname=rtr
vm config net phones,virtio-net-pci
vm launch kvm rtr
vm start rtr

# delivered over miniccc once the router checks in
router rtr interface 0 192.168.20.1/24
router rtr commit

# --- Android VM configuration (launched by the tests) ------------------------

clear vm config

# same as hw.cpu.ncore and hw.ramSize in the AVD's config.ini
vm config vcpus 2
vm config memory 2560

# android-sdk only sets the emulator's environment; the tools need their own paths
vm config android-sdk /usr/local/lib/android/sdk
vm config android-emulator /usr/local/lib/android/sdk/emulator/emulator
vm config android-adb /usr/local/lib/android/sdk/platform-tools/adb
vm config android-avd mmtest
vm config android-avd-dir /usr/local/lib/android/avd
vm config android-no-window true
vm config android-extra-args -no-audio -no-boot-anim -gpu swiftshader_indirect -no-snapshot -camera-back none

vm config net phones
