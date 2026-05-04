# ImageThat

## Rules

- Never rewrite or replace a working system with a completely different approach. If something works, fix the specific issue — don't rip it out.
- Keychain + LAContext is the correct approach for Touch ID vault passwords. Do not replace it with file-based encryption, Secure Enclave key management, or any other custom password storage. Fix bugs in the existing implementation instead.
- Decrypted vault content must never be written to disk. All decrypted data stays in memory.

```
  make build  
  make run                                                                                                  
  make clean 
```
