{
  "procName": "true",
  "exception": {
    "codes": "0x0000000000000000, 0x0000000000000000",
    "rawCodes": [
      0,
      0
    ],
    "type": "EXC_CRASH",
    "signal": "SIGABRT"
  },
  "termination": {
    "namespace": "<0x23>",
    "flags": 582,
    "code": 2
  },
  "asi": null,
  "crashedThreads": [
    {
      "frames": [
        {
          "symbol": "__abort_with_payload",
          "image": "dyld",
          "imageOffset": 484960
        },
        {
          "symbol": "abort_with_payload_wrapper_internal",
          "image": "dyld",
          "imageOffset": 558728
        },
        {
          "symbol": "abort_with_reason",
          "image": "dyld",
          "imageOffset": 558624
        },
        {
          "symbol": "ignition_halt",
          "image": "dyld",
          "imageOffset": 460916
        },
        {
          "symbol": "boot_boot",
          "image": "dyld",
          "imageOffset": 465612
        },
        {
          "symbol": "ignite",
          "image": "dyld",
          "imageOffset": 463708
        },
        {
          "symbol": "dyld4::CacheFinder::CacheFinder(dyld4::ProcessConfig::Process const&, dyld4::ProcessConfig::Logging const&, dyld4::SyscallDelegate&)",
          "image": "dyld",
          "imageOffset": 47396
        },
        {
          "symbol": "dyld4::ProcessConfig::DyldCache::DyldCache(dyld4::ProcessConfig::Process&, dyld4::ProcessConfig::Security const&, dyld4::ProcessConfig::Logging const&, dyld4::SyscallDelegate&, lsl::Allocator&, dyld4::ProcessConfig const&)",
          "image": "dyld",
          "imageOffset": 48416
        },
        {
          "symbol": "dyld4::ProcessConfig::ProcessConfig(dyld4::KernelArgs const*, dyld4::SyscallDelegate&, lsl::Allocator&)",
          "image": "dyld",
          "imageOffset": 37848
        },
        {
          "symbol": "dyld4::start(dyld4::KernelArgs*, void*, void*)::$_0::operator()() const",
          "image": "dyld",
          "imageOffset": 28980
        },
        {
          "symbol": "start",
          "image": "dyld",
          "imageOffset": 27296
        }
      ]
    }
  ]
}
