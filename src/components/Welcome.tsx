import React from 'react';
import { useNostrVersions } from './NostrVersionProvider';
import styled from 'styled-components';
import Card from './common/Card';
import Button from './common/Button';

interface WelcomeProps {
  onStart: () => void;
}

const WelcomeContainer = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: ${props => props.theme.space.lg};
`;

const LogoContainer = styled.div`
  margin-bottom: ${props => props.theme.space.xl};
  text-align: center;

  svg {
    width: 120px;
    height: auto;
  }
`;

const Title = styled.h1`
  font-size: ${props => props.theme.fontSizes['3xl']};
  color: ${props => props.theme.colors.text};
  margin-bottom: ${props => props.theme.space.sm};
  text-align: center;
`;

const Description = styled.p`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: ${props => props.theme.space.xl};
  text-align: center;
  max-width: 600px;
`;

const Features = styled.ul`
  margin-bottom: ${props => props.theme.space.xl};
  padding: 0;
  list-style-type: none;
`;

const Feature = styled.li`
  display: flex;
  align-items: center;
  margin-bottom: ${props => props.theme.space.md};
  
  &:before {
    content: '✓';
    color: ${props => props.theme.colors.primary};
    font-weight: bold;
    margin-right: ${props => props.theme.space.sm};
  }
`;

const ActionButton = styled(Button)`
  margin-top: ${props => props.theme.space.lg};
`;

const VersionsContainer = styled.div`
  margin-top: ${props => props.theme.space.lg};
  border-top: 1px solid ${props => props.theme.colors.border};
  padding-top: ${props => props.theme.space.md};
`;

const VersionTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.md};
  color: ${props => props.theme.colors.text};
  margin-bottom: ${props => props.theme.space.sm};
`;

const VersionsList = styled.ul`
  margin: 0;
  padding: 0;
  list-style-type: none;
`;

const VersionItem = styled.li`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  padding: ${props => props.theme.space.xs} 0;
  display: flex;
  align-items: center;
  
  &:before {
    content: '•';
    color: ${props => props.theme.colors.primary};
    margin-right: ${props => props.theme.space.xs};
  }
`;

const LoadingText = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  font-style: italic;
  padding: ${props => props.theme.space.xs} 0;
`;

const Welcome: React.FC<WelcomeProps> = ({ onStart }) => {
  const { versions, loading, error } = useNostrVersions();
  
  // Function to extract version and model from an event
  const getVersionDetails = (event: any) => {
    const getTollgateVersion = () => {
      const versionTag = event.tags.find((tag: any[]) => tag[0] === 'tollgate_os_version');
      return versionTag && versionTag[1] ? versionTag[1] : 'Unknown';
    };
    
    const getModel = () => {
      const modelTag = event.tags.find((tag: any[]) => tag[0] === 'model');
      return modelTag && modelTag[1] ? modelTag[1] : 'Unknown';
    };
    
    const getOpenWrtVersion = () => {
      const versionTag = event.tags.find((tag: any[]) => tag[0] === 'openwrt_version');
      return versionTag && versionTag[1] ? versionTag[1] : 'Unknown';
    };
    
    return {
      tollgateVersion: getTollgateVersion(),
      model: getModel(),
      openWrtVersion: getOpenWrtVersion()
    };
  };
  return (
    <WelcomeContainer>
      <Card>
        <LogoContainer>
          {/* We'll create a placeholder for the logo since we don't have the SVG */}
          <div style={{ 
            width: '120px', 
            height: '120px', 
            backgroundColor: '#F97316', 
            borderRadius: '50%',
            margin: '0 auto',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: 'white',
            fontWeight: 'bold',
            fontSize: '24px'
          }}>
            TG
          </div>
        </LogoContainer>
        
        <Title>TollGate Installer</Title>
        <Description>
          Easily install TollGate OS on your fresh router with just a few clicks
        </Description>
        
        <Features>
          <Feature>Automatically detects routers on your network</Feature>
          <Feature>Simple guided installation process</Feature>
          <Feature>Securely transfers and installs TollGate OS</Feature>
          <Feature>Optimized for consecutive router installs</Feature>
        </Features>
        
        <ActionButton 
          variant="primary" 
          size="large" 
          fullWidth 
          onClick={onStart}
        >
          Start Installation
        </ActionButton>
        
        <VersionsContainer>
          <VersionTitle>Available OS Versions</VersionTitle>
          {error ? (
            <LoadingText>Error loading versions: {error}</LoadingText>
          ) : loading ? (
            <LoadingText>Searching for available OS versions...</LoadingText>
          ) : versions.length === 0 ? (
            <LoadingText>No OS versions found.</LoadingText>
          ) : (
            <VersionsList>
              {versions.map((event, index) => {
                const { tollgateVersion, model, openWrtVersion } = getVersionDetails(event);
                return (
                  <VersionItem key={index}>
                    TollGate OS {tollgateVersion} for {model} (OpenWrt {openWrtVersion})
                  </VersionItem>
                );
              })}
            </VersionsList>
          )}
        </VersionsContainer>
      </Card>
    </WelcomeContainer>
  );
};

export default Welcome;