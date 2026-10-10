// SPDX-License-Identifier: AGPL-3.0-only
// Linux pcsc-lite ABI: https://pcsclite.apdu.fr/api/group__API.html
// Compile: cc -std=c11 -O2 -Wall -Wextra -Werror -o nfc-pcsc-bridge nfc-pcsc-bridge.c -ldl
// No keys, shell commands, network, selection fallback or arbitrary reader discovery.
// Parent runs this process under prlimit and a hard deadline. One exclusive card
// connection survives the bounded APDU exchange, then is powered down on close.
#define _POSIX_C_SOURCE 200809L
#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
typedef unsigned long DWORD;
typedef long LONG;
typedef struct { DWORD protocol, length; } PCI;
static int digit(char c) { if(c>='0'&&c<='9')return c-'0'; if(c>='a'&&c<='f')return c-'a'+10; return -1; }
int main(int argc, char **argv) {
 if(argc!=3 || strlen(argv[2])<1 || strlen(argv[2])>127 ||
    (strcmp(argv[1],"ready") && strcmp(argv[1],"exchange"))) return 2;
 void *lib=dlopen("libpcsclite.so.1", RTLD_NOW|RTLD_LOCAL); if(!lib)return 3;
 LONG (*establish)(DWORD,const void*,const void*,LONG*)=dlsym(lib,"SCardEstablishContext");
 LONG (*release)(LONG)=dlsym(lib,"SCardReleaseContext");
 LONG (*list)(LONG,const char*,char*,DWORD*)=dlsym(lib,"SCardListReaders");
 LONG (*connect)(LONG,const char*,DWORD,DWORD,LONG*,DWORD*)=dlsym(lib,"SCardConnect");
 LONG (*reconnect)(LONG,DWORD,DWORD,DWORD,DWORD*)=dlsym(lib,"SCardReconnect");
 LONG (*disconnect)(LONG,DWORD)=dlsym(lib,"SCardDisconnect");
 LONG (*transmit)(LONG,const PCI*,const unsigned char*,DWORD,PCI*,unsigned char*,DWORD*)=dlsym(lib,"SCardTransmit");
 if(!establish||!release||!list||!connect||!reconnect||!disconnect||!transmit)return 3;
 LONG ctx=0,card=0; DWORD protocol=0; int status=4;
 if(establish(2,NULL,NULL,&ctx))goto done;
 char names[4096]={0}; DWORD size=sizeof(names);
 if(list(ctx,NULL,names,&size)||size>sizeof(names)||size<2||names[size-1]||names[size-2])goto done;
 int found=0; for(size_t n=0;n+1<size;) { size_t len=strnlen(names+n,size-n); if(len==size-n)goto done; if(!strcmp(names+n,argv[2]))found=1; n+=len+1; }
 if(!found)goto done;
 if(!strcmp(argv[1],"ready")) { puts("ready"); status=0; goto done; }
 if(connect(ctx,argv[2],1,2,&card,&protocol)||protocol!=2)goto done;
 // A previous killed client may not have run its disconnect. Start a fresh RF
 // session before the SUN read rather than assuming its counter advanced.
 if(reconnect(card,1,2,2,&protocol)||protocol!=2)goto done;
 PCI pci={protocol,sizeof(PCI)};
 char line[525]; unsigned char request[261],response[258];
 for(unsigned count=0;count<16;count++) {
  if(!fgets(line,sizeof(line),stdin)) { status=feof(stdin)?0:4; goto done; }
  size_t length=strlen(line); if(length<11||line[length-1]!='\n'||(length-1)%2||length>523)goto done;
  length=(length-1)/2;
  for(size_t i=0;i<length;i++){int hi=digit(line[2*i]),lo=digit(line[2*i+1]); if(hi<0||lo<0)goto done; request[i]=(unsigned char)((hi<<4)|lo);}
  DWORD received=sizeof(response); if(transmit(card,&pci,request,length,NULL,response,&received)||received<2||received>sizeof(response))goto done;
  for(DWORD i=0;i<received;i++)printf("%02x",response[i]);
  putchar('\n'); if(fflush(stdout))goto done;
 }
done:
 if(card)disconnect(card,2);
 if(ctx)release(ctx);
 dlclose(lib);
 return status;
}
