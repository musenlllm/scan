这段代码主要实现了对HTTP响应中的CDN（内容分发网络）检测功能。下面是对每个部分的详细解释：

初始化（init）函数
定义Ranger变量：var ranger cidranger.Ranger 定义了一个cidranger.Ranger类型的变量ranger，用于存储CIDR（无类别域间路由）范围。
初始化Ranger：ranger = cidranger.NewPCTrieRanger() 初始化ranger，使用cidranger库中的NewPCTrieRanger函数创建一个新的PCTrieRanger实例。
遍历并插入CIDR：通过遍历cdnIpCidr（未在代码中定义，可能是全局变量或外部传入的数据）中的每个CIDR字符串，解析每个CIDR并插入到ranger中。
CDNCheck函数
参数：

r *Response：HTTP响应对象。
ip string：目标IP地址。
canme string：CNAME记录。
检查CNAME：遍历cdnCnameKeywords（未在代码中定义，可能是全局变量或外部传入的数据），检查CNAME是否包含关键字，如果是则返回包含CDN关键字的字符串。

检查IP地址：使用ranger.Contains检查IP地址是否在预定义的CIDR范围内。如果在，则返回包含CIDR范围的字符串。

检查HTTP头：如果响应对象不为空且包含头信息，遍历头信息并检查是否包含cdnHeaderKeys（未在代码中定义，可能是全局变量或外部传入的数据）中的关键字，如果是则返回包含CDN关键字的字符串。

默认返回：如果以上检查都未找到CDN，则返回"否"和一个错误。

StrInSlice函数
功能：检查一个字符串是否在一个字符串切片中。

参数：

i string：要检查的字符串。
array []string：字符串切片。
实现：遍历切片，如果找到匹配的字符串则返回true，否则返回false。

总结
这段代码主要实现了一个HTTP响应的CDN检测功能，通过检查CNAME、IP地址和HTTP头信息来确定是否使用了CDN。使用了cidranger库来存储和检查CIDR范围，以便快速确定IP地址是否在特定的CIDR范围内。