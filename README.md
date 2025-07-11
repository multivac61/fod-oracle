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



## Example

```console
$ fod-oracle 'github:NixOS/nixpkgs#legacyPackages.x86_64-linux.hello' --rebuild
{"drv_path":"/nix/store/cxh8cwlp0z4a7xl6bk7zsqr7ci1hzdq6-make-4.4.1.tar.gz.drv","output_path":"/nix/store/0avnvyc7pkcr4pjqws7hwpy87m6wlnjc-make-4.4.1.tar.gz","hash_algorithm":"sha256","hash":"sha256-3Rb7HWe/q3mnL16DkHNcSePo5wtJRaFasfgd23hlj7M="}
{"drv_path":"/nix/store/rslimz8ksg82kp85hfmjrinhpi3s8mzn-bash52-032.drv","output_path":"/nix/store/mr1mf5qdyw8v3rxy0w0pca94h5cmmvgr-bash52-032","hash_algorithm":"sha256","hash":"sha256-e5x32uypP/cReB11NyNBZug+2YNc4e59zVdCMZw3KhY="}
{"drv_path":"/nix/store/z2rvjzfm2crd6hs6qfdwwq4bhk27zw9r-perl-5.40.0.tar.gz.drv","output_path":"/nix/store/4b4l8a41cvmz9z871gkc4jjpifxs8n8n-perl-5.40.0.tar.gz","hash_algorithm":"sha256","hash":"sha256-x0A0jzVzljJ6l5XT6DI7r9D+ilx4NfwcuroMyN/nFh8="}
{"drv_path":"/nix/store/lrxrk69f3d3rpxf43sfibv3cfwwlc2ra-bash52-026.drv","output_path":"/nix/store/yzd1kmz6qlk3dxa82lwfbgqqxy2c4m1x-bash52-026","hash_algorithm":"sha256","hash":"sha256-lu4fVJqgtTBSHja9wLp2YWAs+u5An3AjysdE3UKFLqw="}
{"drv_path":"/nix/store/ag9cnvb4pcgcj0rbkzva6qdz54fnr8fg-bash52-012.drv","output_path":"/nix/store/x1sqwqn02c5mnpi8hbqlxpbm3rahq5dm-bash52-012","hash_algorithm":"sha256","hash":"sha256-L7EHzh+46T82mXyLCydD/BypikVMfMWj/KvsUz9n1Cw="}
{"drv_path":"/nix/store/mag3pq29n9f6jw0rb17crny8ilm4kfjy-diffutils-3.12.tar.xz.drv","output_path":"/nix/store/h8xhahhcdawxq5xg0niqi044l7fyvppq-diffutils-3.12.tar.xz","hash_algorithm":"sha256","hash":"sha256-fIt/n8hgkUH96pzs6FJJ0whiQ5H/Yd7a9Sj8szdyff0="}
...
...
```

## Hash Verification

When rebuilds succeed, it means Nix has verified the FOD hash matches. When rebuilds fail due to hash mismatches, the tool extracts both the expected and actual hashes from Nix's error output for easy comparison.

**Hash mismatch example:**

```json
{"drv_path":"/nix/store/...","rebuild_status":"failure","hash_mismatch":true,"actual_hash":"sha256-ActualHashHere","error_message":"Build failed: ..."}
```
