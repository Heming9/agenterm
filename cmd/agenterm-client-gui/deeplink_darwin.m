#import <Cocoa/Cocoa.h>
#import <CoreServices/CoreServices.h>
extern void agentermOpenURL(const char *value);
@interface AgentermURLHandler : NSObject
- (void)handle:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply;
@end
@implementation AgentermURLHandler
- (void)handle:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
 NSString *value = [[event paramDescriptorForKeyword:keyDirectObject] stringValue];
 if (value) agentermOpenURL([value UTF8String]);
}
@end
void installAgentermURLHandler(void) {
 static AgentermURLHandler *handler;
 handler = [AgentermURLHandler new];
 [[NSAppleEventManager sharedAppleEventManager] setEventHandler:handler andSelector:@selector(handle:withReplyEvent:) forEventClass:kInternetEventClass andEventID:kAEGetURL];
}
int registerAgentermBundle(const char *path) {
 NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
 return (int)LSRegisterURL((__bridge CFURLRef)url, true);
}
