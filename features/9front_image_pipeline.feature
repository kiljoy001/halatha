Feature: 9front Halatha image pipeline
  9front should be authored as a raw disk image in QEMU and served unchanged to
  v86, with o9c, Yggdrasil startup, and host 9P support available to the guest.

  Scenario: Create a raw image for 9front installation
    Given the developer has qemu-img installed
    When the developer runs the 9front disk creation script
    Then a raw disk image is created without overwriting an existing image
    And the script prints the exact byte size for the v86 URL

  Scenario: Package Halatha software for installation inside 9front
    Given the objective-9c release/386 directory exists
    When the developer builds the Halatha 9front payload ISO
    Then the payload contains /386/bin/o9c
    And the payload contains libo9.a, o9.h, o9 rc helpers, runtime sources, and stdlib modules
    And the payload contains halatha.local, ygg-up, and yggdrasil.peers

  Scenario: Include Yggdrasil binaries when they are supplied
    Given Plan 9 386 yggdrasil and yggdrasilctl binaries are available
    When the developer builds the payload with YGG_BIN and YGGCTL_BIN
    Then the payload contains /386/bin/yggdrasil
    And the payload contains /386/bin/yggdrasilctl

  Scenario: Install the payload into an installed 9front system
    Given 9front is installed on the raw target disk
    And the Halatha payload ISO is mounted in the guest
    When the developer runs install-halatha.rc
    Then o9c is installed as /386/bin/o9c
    And Yggdrasil startup files are installed
    And termrc starts the host-served halatha.local hook when /n/host is mounted
    And termrc falls back to the image-local /rc/bin/halatha.local

  Scenario: Serve the customized image in v86
    Given the customized 9front raw image is available over HTTP with Range support
    When the user opens the 9front direct v86 page with the image URL and byte size
    Then v86 boots the image as hda with boot=c
    And mouse capture works by clicking the VM canvas

  Scenario: Configure a browser terminal for an existing Plan 9 grid
    Given a live Plan 9 grid has auth, cpu, and file servers reachable over Yggdrasil
    And the installed 9front image has Yggdrasil binaries
    When the developer runs halatha-grid-terminal.rc in the installed image
    Then the image starts IP networking at boot
    And the image starts Yggdrasil after networking is up
    And the image provides a grid command for cpu login
    And the image can import the configured file server at /n/grid

  Scenario: Mount the browser host namespace over virtio 9P
    Given the 9front guest kernel includes the devvirtio9p device
    When the guest mounts '#9/host9p' or '#9/0' at /n/host
    Then /n/host/READY is visible
    And /n/host/boot contains the host-served Halatha startup scripts

  Scenario: Detect missing guest virtio 9P support
    Given the 9front guest kernel does not include the devvirtio9p device
    When the guest tries to mount '#9/host9p'
    Then the mount fails because the #9 device is absent
    And the next pipeline task is to rebuild the 9front kernel or bake serial9p as a fallback
