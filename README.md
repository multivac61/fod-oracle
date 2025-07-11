<div align="center">

# fod-oracle

  <img src="./docs/sibyl.webp" height="150"/>


<p>
<img alt="Static Badge" src="https://img.shields.io/badge/Status-experimental-orange">
</p>

</div>

> Temet Nosce

## Overview

FOD Oracle finds all Fixed Output Derivations (FODs) in a Nix expression. It helps identify discrepancies and changes in FODs that might indicate issues with build reproducibility.

## Features

- **FOD Tracking**: Scans nixpkgs revisions and tracks all fixed-output derivations
- **JSON Lines Streaming**: Outputs FODs as streaming JSON Lines for real-time processing
- **FOD Rebuilding**: Verifies FOD integrity by rebuilding and comparing hashes

## Usage

```console
fod-oracle finds all Fixed Output Derivations (FODs) in a Nix expression.                                             
                                                                                                                      
FODs are derivations with predetermined output hashes, typically used for source code,                                
patches, and other fixed content that needs to be downloaded from external sources.                                   
       
USAGE  
       
  fod-oracle <expression> [command] [--flags]                                                     
          
EXAMPLES  
          
  # Find FODs in a flake package                                                                  
  fod-oracle 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'                             
                                                                                                  
  # Find FODs and rebuild them to verify hashes                                                   
  fod-oracle --rebuild 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'                   
                                                                                                  
  # Rebuild and fail if any rebuild fails for any reason                                          
  fod-oracle --rebuild --strict-rebuild 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'  
                                                                                                  
  # Use a regular Nix expression                                                                  
  fod-oracle --expr 'import <nixpkgs> {}.hello'                                                   
                                                                                                  
  # Enable debug output                                                                           
  fod-oracle --debug 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'                     
          
COMMANDS  
          
  completion [command]  Generate the autocompletion script for the specified shell
  help [command]        Help about any command
       
FLAGS  
       
  --debug               Enable debug output to stderr
  --expr                Treat the argument as a Nix expression
  --flake               Evaluate a flake expression
  -h --help             Help for fod-oracle
  --max-parallel        Maximum number of parallel rebuilds (default: CPU count)
  --rebuild             Rebuild FODs to verify their hashes
  --strict-rebuild      Exit with error code if any rebuild fails for any reason (requires --rebuild)
  -v --version          Version for fod-oracle
```



## Examples

```console
$ fod-oracle 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello'
{"drv_path":"/nix/store/z7rvbcngls4p928an3ajiawypmhd9rw1-bash52-028.drv","output_path":"/nix/store/m2vybc6frn326f6yxlhpkbvi7grjzna2-bash52-028","hash_algorithm":"sha256","hash":"sha256-YEJ4C6KJPayko/D5tlcoWSzXu21M6+BzhVpqrU1jqsE="}
{"drv_path":"/nix/store/jxaa5p13rvgwbxm14yqmfxnj3rfj322f-bash52-037.drv","output_path":"/nix/store/yp6bfms6q369y93j622mqq6i6b02xwbq-bash52-037","hash_algorithm":"sha256","hash":"sha256-iiwcO1El2a5bR4gvfS3flkiAX4xnwTql6n7+rEdc2pQ="}
{"drv_path":"/nix/store/bsf32582y9rl8wy0dmi6h1lfd0f3qklh-lzip-1.25.tar.gz.drv","output_path":"/nix/store/8jaj1xfcj1i3sl6m8zh2wn8k0rzxq9nh-lzip-1.25.tar.gz","hash_algorithm":"sha256","hash":"sha256-CUGKbY+4P1ET9b2FbglwPfXTe64DCMZo0PNG49PwpW8="}
{"drv_path":"/nix/store/v6b5sjrlhljhrvk4dy9f786riml2nn8k-hello-2.12.2.tar.gz.drv","output_path":"/nix/store/dw402azxjrgrzrk6j0p66wkqrab5mwgw-hello-2.12.2.tar.gz","hash_algorithm":"sha256","hash":"sha256-WpqZbcKSzCTc9BHO6H6S9qrluNE72caBm0x6nc4IGKs="}
...
```


```console
$ fod-oracle 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello' --rebuild
{"drv_path":"/nix/store/2k5fih5nlvvwafb9qivnvxfiv1170ihb-source.drv","output_path":"/nix/store/vyxhyk3i20rzdq4clbzpl31kxs8yprmm-source","hash_algorithm":"r:sha256","hash":"sha256-7Niq+Xxq/r86qOeJl6/gNdH1XKm6m0fPhbPmgazZFkU=","rebuild_status":"success","actual_hash":"sha256-7Niq+Xxq/r86qOeJl6/gNdH1XKm6m0fPhbPmgazZFkU="}
{"drv_path":"/nix/store/h46szk9s9lmwygw3bkif0dwr51xbycjx-source.drv","output_path":"/nix/store/j27ci7p95zf84mb5sqyjjmcpd1sfazkp-source","hash_algorithm":"r:sha256","hash":"sha256-bfFmDfRBSvoWMdQYVstsJRbcq+15lDjVFqk+0XYWpy8=","rebuild_status":"success","actual_hash":"sha256-bfFmDfRBSvoWMdQYVstsJRbcq+15lDjVFqk+0XYWpy8="}
{"drv_path":"/nix/store/qrhjpcmqb46fzcfjfdbmjbibv787ysmh-xnu-src.drv","output_path":"/nix/store/ryh5cxpq72p3b8jjg16s10cw0nl5k8dv-xnu-src","hash_algorithm":"r:sha256","hash":"sha256-uHmAOm6k9ZXWfyqHiDSpm+tZqUbERlr6rXSJ4xNACkM=","rebuild_status":"success","actual_hash":"sha256-uHmAOm6k9ZXWfyqHiDSpm+tZqUbERlr6rXSJ4xNACkM="}
{"drv_path":"/nix/store/36mp6b0ybqy6z3jhk36lnd8gpkdsw9vl-macOS-SDK-11.3.drv","output_path":"/nix/store/iak05hjjbnrzfdavj463scmf9cndidlf-macOS-SDK-11.3","hash_algorithm":"r:sha256","hash":"sha256-/go8utcx3jprf6c8V/DUbXwsmNYSFchOAai1OaJs3Bg=","rebuild_status":"success","actual_hash":"sha256-/go8utcx3jprf6c8V/DUbXwsmNYSFchOAai1OaJs3Bg="}
{"drv_path":"/nix/store/2srmqir9grsxxcjfknxxxlf1y1ysmv9v-macOS-SDK-14.4.drv","output_path":"/nix/store/c3xfmn0gi4zss7yzgh2k969xcjjyv5p0-macOS-SDK-14.4","hash_algorithm":"r:sha256","hash":"sha256-QozDiwY0Czc0g45vPD7G4v4Ra+3DujCJbSads3fJjjM=","rebuild_status":"success","actual_hash":"sha256-QozDiwY0Czc0g45vPD7G4v4Ra+3DujCJbSads3fJjjM="}
...
```

## Hash Verification

When rebuilds succeed, it means Nix has verified the FOD hash matches. When rebuilds fail due to hash mismatches, the tool extracts both the expected and actual hashes from Nix's error output for easy comparison.

**Hash mismatch example:**

```json
{"drv_path":"/nix/store/...","rebuild_status":"failure","hash_mismatch":true,"actual_hash":"sha256-ActualHashHere","error_message":"Build failed: ..."}
```
