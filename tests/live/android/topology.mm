# minimega live integration test topology: Android
#
# Configuration only. tests/live/android launches the VM itself, so a failed
# launch is a test failure with the emulator log kept, not a failed "mm read".
#
# The SDK and the AVD are not in the image: run.sh mounts them at the same
# path from the host (LIVE_DOCKER_ARGS="-v /usr/local/lib/android:/usr/local/lib/android").
# The paths, the AVD name and the hardware below match what
# .github/workflows/live-android-spike.yml installs.

namespace live
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
